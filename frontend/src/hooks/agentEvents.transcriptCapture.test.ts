import { create } from '@bufbuild/protobuf'
import { createRoot, onCleanup } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { AgentChatMessageSchema, AgentProvider, ContentCompression, MessageSource, TodoItemSchema, TodoStatus } from '~/generated/proto/leapmux/v1/agent_pb'
import { handleAgentMessage } from '~/hooks/agentEvents'
import { createAgentSessionStore } from '~/stores/agentSession.store'
import { createChatStore, MAX_BACKGROUND_CHAT_MESSAGES } from '~/stores/chat.store'
import { installTestBridge } from '~/test-support/crdtBridge'
import { createTestTabStores } from '~/test-support/tabStores'
import '~/components/chat/providers'

const agentId = 'captured-transcript-agent'
const identity = { spanId: 'current-tool', agentSessionId: 'native-session' }

function withCaptureRoot(run: () => void) {
  createRoot((dispose) => {
    try {
      run()
    }
    finally {
      dispose()
    }
  })
}

describe('withCaptureRoot', () => {
  it('disposes the captured stores after a successful callback', () => {
    let cleaned = false
    withCaptureRoot(() => {
      storesForCapture()
      onCleanup(() => {
        cleaned = true
      })
    })
    expect(cleaned).toBe(true)
  })

  it('disposes the captured stores when an assertion throws', () => {
    let cleaned = false
    expect(() => withCaptureRoot(() => {
      storesForCapture()
      onCleanup(() => {
        cleaned = true
      })
      expect(false).toBe(true)
    })).toThrow(/expected false to be true/)
    expect(cleaned).toBe(true)
  })
})

function storesForCapture(includeUsage = true) {
  installTestBridge({ workspaceId: 'captured-transcript-workspace' })
  const tabs = createTestTabStores('captured-transcript-workspace')
  const stores = {
    ...tabs,
    agentSessionStore: createAgentSessionStore(),
    chatStore: createChatStore(),
    getActiveWorkspaceId: () => 'captured-transcript-workspace',
  }
  stores.agentSessionStore.applyProgress(agentId, { revision: 10, thinkingTokens: 17, output: { bytes: 8, minimum: false } })
  if (includeUsage)
    stores.agentSessionStore.updateInfo(agentId, { totalCostUsd: 2, contextUsage: { inputTokens: 100, cacheCreationInputTokens: 0, cacheReadInputTokens: 0 } })
  stores.chatStore.applyToolProgress(agentId, { ...identity, outputTail: 'current output' })
  stores.chatStore.todos.replace(agentId, [create(TodoItemSchema, { id: 'current-task', content: 'Current task', status: TodoStatus.IN_PROGRESS })])
  return stores
}

function message(content: unknown, transcriptOnly: boolean, source = MessageSource.AGENT) {
  return create(AgentChatMessageSchema, {
    id: 'old-captured-row',
    seq: 20n,
    agentProvider: AgentProvider.CLAUDE_CODE,
    source,
    content: new TextEncoder().encode(JSON.stringify(content)),
    contentCompression: ContentCompression.NONE,
    agentSessionId: 'native-session',
    transcriptOnly,
  })
}

describe('handleAgentMessage', () => {
  it.each([
    { label: 'ordinary live', transcriptOnly: false, phase: 'live' },
    { label: 'ordinary replay', transcriptOnly: false, phase: 'catchingUp' },
    { label: 'transcript-only live', transcriptOnly: true, phase: 'live' },
    { label: 'transcript-only replay', transcriptOnly: true, phase: 'catchingUp' },
  ] as const)('retains a $label row without applying its current-state effects', ({ transcriptOnly, phase }) => {
    withCaptureRoot(() => {
      const stores = storesForCapture()
      stores.agentSessionStore.beginReplay(agentId, 1n)
      stores.agentSessionStore.updateInfo(agentId, { planFilePath: '/current-plan' })
      stores.metadata.patch(agentId, { title: 'Current title' })
      const row = message({ type: 'result', subtype: 'success', total_cost_usd: 9 }, transcriptOnly)
      row.supplementalContent = new TextEncoder().encode(JSON.stringify({ metadata: { total_cost_usd: 8, context_usage: { input_tokens: 9 } } }))
      row.supplementalContentCompression = ContentCompression.NONE
      const content = row.content.slice()
      const supplemental = row.supplementalContent.slice()
      handleAgentMessage(agentId, row, stores, phase, 1n, 'retain-transcript')
      expect(stores.agentSessionStore.getInfo(agentId)).toMatchObject({ totalCostUsd: 2, contextUsage: { inputTokens: 100 }, planFilePath: '/current-plan' })
      expect(stores.agentSessionStore.getProgress(agentId)).toEqual({ revision: 10, thinkingTokens: 17, output: { bytes: 8, minimum: false } })
      expect(stores.chatStore.getToolProgress(agentId, identity)?.outputTail).toBe('current output')
      expect(stores.chatStore.todos.getById(agentId, 'current-task')?.status).toBe('in_progress')
      expect(stores.metadata.get(agentId)?.title).toBe('Current title')
      expect(stores.chatStore.getMessages(agentId)[0]?.id).toBe(row.id)
      expect(row.content).toEqual(content)
      expect(row.supplementalContent).toEqual(supplemental)
      expect(row.transcriptOnly).toBe(transcriptOnly)
    })
  })

  it.each(['context_cleared', 'plan_updated', 'tool_result'] as const)('retains a refused %s row without metadata or cleanup', (kind) => {
    withCaptureRoot(() => {
      const stores = storesForCapture()
      stores.agentSessionStore.updateInfo(agentId, { planFilePath: '/current-plan' })
      stores.metadata.patch(agentId, { title: 'Current title' })
      const row = message(kind === 'plan_updated'
        ? { type: kind, plan_file_path: '/old-plan', plan_title: 'Old title', update_agent_title: true }
        : kind === 'tool_result'
          ? { type: 'user', message: { content: [{ type: kind, tool_use_id: identity.spanId, content: 'Old output' }] } }
          : { type: kind }, false, kind === 'tool_result' ? MessageSource.AGENT : MessageSource.LEAPMUX)
      row.spanId = identity.spanId
      const content = row.content.slice()
      handleAgentMessage(agentId, row, stores, 'live', 0n, 'retain-transcript')
      expect(stores.agentSessionStore.getInfo(agentId)).toMatchObject({ totalCostUsd: 2, contextUsage: { inputTokens: 100 }, planFilePath: '/current-plan' })
      expect(stores.agentSessionStore.getProgress(agentId).thinkingTokens).toBe(17)
      expect(stores.chatStore.getToolProgress(agentId, identity)?.outputTail).toBe('current output')
      expect(stores.chatStore.todos.getById(agentId, 'current-task')?.status).toBe('in_progress')
      expect(stores.metadata.get(agentId)?.title).toBe('Current title')
      expect(stores.chatStore.getMessages(agentId)[0]?.id).toBe(row.id)
      expect(row.content).toEqual(content)
      expect(row.transcriptOnly).toBe(false)
    })
  })

  it('consumes refused ephemeral session info without a transcript row or current-state effect', () => {
    withCaptureRoot(() => {
      const stores = storesForCapture()
      stores.agentSessionStore.beginReplay(agentId, 1n)
      stores.chatStore.goal.setProgress(agentId, { tokensUsed: 150 })
      const row = message({ type: 'agent_session_info', info: { total_cost_usd: 9, generation_progress_revision: 11, thinking_tokens: 0, running_tool: { span_id: identity.spanId, agent_session_id: identity.agentSessionId, output_tail: 'Old output' }, goal_progress: { tokens_used: 100 } } }, false, MessageSource.LEAPMUX)
      row.seq = -1n
      const content = row.content.slice()
      handleAgentMessage(agentId, row, stores, 'catchingUp', 1n, 'retain-transcript')
      expect(stores.agentSessionStore.getInfo(agentId).totalCostUsd).toBe(2)
      expect(stores.agentSessionStore.getProgress(agentId)).toEqual({ revision: 10, thinkingTokens: 17, output: { bytes: 8, minimum: false } })
      expect(stores.chatStore.getToolProgress(agentId, identity)?.outputTail).toBe('current output')
      expect(stores.chatStore.goal.progress(agentId)).toEqual({ tokensUsed: 150 })
      expect(stores.chatStore.getMessages(agentId)).toEqual([])
      expect(row.content).toEqual(content)
      expect(row.transcriptOnly).toBe(false)
    })
  })

  it('trims background history after a refused transcript row arrives', () => {
    withCaptureRoot(() => {
      const stores = storesForCapture()
      stores.chatStore.setMessages(agentId, Array.from({ length: MAX_BACKGROUND_CHAT_MESSAGES }, (_, index) => create(AgentChatMessageSchema, { id: `history-${index}`, seq: BigInt(index + 1), source: MessageSource.USER })))
      const row = message({ type: 'assistant', total_cost_usd: 9 }, false)
      row.seq = BigInt(MAX_BACKGROUND_CHAT_MESSAGES + 1)
      handleAgentMessage(agentId, row, stores, 'live', 0n, 'retain-transcript')
      expect(stores.chatStore.getMessages(agentId)).toHaveLength(MAX_BACKGROUND_CHAT_MESSAGES)
      expect(stores.chatStore.getMessages(agentId)[0]?.id).toBe('history-1')
      expect(stores.chatStore.getMessages(agentId).at(-1)?.id).toBe(row.id)
      expect(stores.agentSessionStore.getInfo(agentId).totalCostUsd).toBe(2)
    })
  })

  it.each([150, 0])('keeps live goal tokens %s when an older snapshot arrives on the same receipt', (tokensUsed) => {
    withCaptureRoot(() => {
      const stores = storesForCapture(false)
      stores.agentSessionStore.beginReplay(agentId, 1n)
      const live = message({ type: 'agent_session_info', info: { goal_progress: { tokens_used: tokensUsed } } }, false, MessageSource.LEAPMUX)
      live.seq = -1n
      live.agentProvider = AgentProvider.UNSPECIFIED
      handleAgentMessage(agentId, live, stores, 'live')
      expect(stores.chatStore.goal.progress(agentId).tokensUsed).toBe(tokensUsed)
      const snapshot = message({ type: 'agent_session_info', info: { goal_progress: { tokens_used: 100 } } }, false, MessageSource.LEAPMUX)
      snapshot.seq = -1n
      snapshot.agentProvider = AgentProvider.UNSPECIFIED
      handleAgentMessage(agentId, snapshot, stores, 'catchingUp', 1n)
      expect(stores.chatStore.goal.progress(agentId).tokensUsed).toBe(tokensUsed)
    })
  })

  it('restores unclaimed goal counters beside a live counter on the same receipt', () => {
    withCaptureRoot(() => {
      const stores = storesForCapture(false)
      stores.agentSessionStore.beginReplay(agentId, 1n)
      const live = message({ type: 'agent_session_info', info: { goal_progress: { tokens_used: 150 } } }, false, MessageSource.LEAPMUX)
      live.seq = -1n
      handleAgentMessage(agentId, live, stores, 'live')
      const snapshot = message({ type: 'agent_session_info', info: { goal_progress: { tokens_used: 100, iterations: 3, time_used_seconds: 0 } } }, false, MessageSource.LEAPMUX)
      snapshot.seq = -1n
      handleAgentMessage(agentId, snapshot, stores, 'catchingUp', 1n)
      expect(stores.chatStore.goal.progress(agentId)).toEqual({ tokensUsed: 150, iterations: 3, timeUsedSeconds: 0 })
    })
  })

  it('restores every present cold goal counter including zero', () => {
    withCaptureRoot(() => {
      const stores = storesForCapture(false)
      stores.agentSessionStore.beginReplay(agentId, 1n)
      const snapshot = message({ type: 'agent_session_info', info: { goal_progress: { tokens_used: 0, token_budget: 200, time_used_seconds: 3, iterations: 4 } } }, false, MessageSource.LEAPMUX)
      snapshot.seq = -1n
      handleAgentMessage(agentId, snapshot, stores, 'catchingUp', 1n)
      expect(stores.chatStore.goal.progress(agentId)).toEqual({ tokensUsed: 0, tokenBudget: 200, timeUsedSeconds: 3, iterations: 4 })
    })
  })

  it.each(['completed', 'superseded'] as const)('keeps goal progress when a %s receipt sends another snapshot', (reason) => {
    withCaptureRoot(() => {
      const stores = storesForCapture(false)
      stores.agentSessionStore.beginReplay(agentId, 1n)
      stores.chatStore.goal.setProgress(agentId, { tokensUsed: 150 })
      if (reason === 'completed')
        stores.agentSessionStore.retireReplay(agentId, 1n)
      else
        stores.agentSessionStore.beginReplay(agentId, 2n)
      const snapshot = message({ type: 'agent_session_info', info: { goal_progress: { tokens_used: 100 } } }, false, MessageSource.LEAPMUX)
      snapshot.seq = -1n
      handleAgentMessage(agentId, snapshot, stores, 'catchingUp', 1n)
      expect(stores.chatStore.goal.progress(agentId).tokensUsed).toBe(150)
    })
  })

  it.each(['live', 'catchingUp'] as const)('keeps current state when a stale divider arrives during %s', (phase) => {
    withCaptureRoot(() => {
      const stores = storesForCapture()
      handleAgentMessage(agentId, message({ type: 'result', subtype: 'success', total_cost_usd: 1 }, true), stores, phase)
      expect(stores.agentSessionStore.getProgress(agentId).thinkingTokens).toBe(17)
      expect(stores.agentSessionStore.getProgress(agentId).output?.bytes).toBe(8)
      expect(stores.chatStore.getToolProgress(agentId, identity)?.outputTail).toBe('current output')
      expect(stores.agentSessionStore.getInfo(agentId).totalCostUsd).toBe(2)
      expect(stores.chatStore.getMessages(agentId).some(row => row.id === 'old-captured-row')).toBe(true)
    })
  })

  it('keeps current to-dos and usage when a stale context clear arrives', () => {
    withCaptureRoot(() => {
      const stores = storesForCapture()
      handleAgentMessage(agentId, message({ type: 'context_cleared' }, true, MessageSource.LEAPMUX), stores, 'live')
      expect(stores.agentSessionStore.getInfo(agentId).contextUsage?.inputTokens).toBe(100)
      expect(stores.agentSessionStore.getInfo(agentId).totalCostUsd).toBe(2)
      expect(stores.chatStore.todos.getById(agentId, 'current-task')?.status).toBe('in_progress')
      expect(stores.agentSessionStore.getProgress(agentId).thinkingTokens).toBe(17)
      expect(stores.chatStore.getMessages(agentId).some(row => row.id === 'old-captured-row')).toBe(true)
    })
  })

  it('keeps replacement progress when a previously current divider replays', () => {
    withCaptureRoot(() => {
      const stores = storesForCapture()
      handleAgentMessage(agentId, message({ type: 'result', subtype: 'success' }, false), stores, 'catchingUp')
      expect(stores.agentSessionStore.getProgress(agentId).thinkingTokens).toBe(17)
      expect(stores.agentSessionStore.getProgress(agentId).output?.bytes).toBe(8)
      expect(stores.chatStore.getToolProgress(agentId, identity)?.outputTail).toBe('current output')
    })
  })

  it('keeps current usage when an older completed turn replays', () => {
    withCaptureRoot(() => {
      const stores = storesForCapture()
      handleAgentMessage(agentId, message({ type: 'result', subtype: 'success', total_cost_usd: 1 }, false), stores, 'catchingUp')
      expect(stores.agentSessionStore.getInfo(agentId).totalCostUsd).toBe(2)
      expect(stores.agentSessionStore.getInfo(agentId).contextUsage?.inputTokens).toBe(100)
      expect(stores.chatStore.getMessages(agentId).some(row => row.id === 'old-captured-row')).toBe(true)
    })
  })

  it('keeps canonical to-dos and live fields when a historical context clear replays', () => {
    withCaptureRoot(() => {
      const stores = storesForCapture()
      handleAgentMessage(agentId, message({ type: 'context_cleared' }, false, MessageSource.LEAPMUX), stores, 'catchingUp')
      expect(stores.chatStore.todos.getById(agentId, 'current-task')?.status).toBe('in_progress')
      expect(stores.agentSessionStore.getInfo(agentId).totalCostUsd).toBe(2)
      expect(stores.agentSessionStore.getInfo(agentId).contextUsage?.inputTokens).toBe(100)
      expect(stores.agentSessionStore.getProgress(agentId).thinkingTokens).toBe(17)
      expect(stores.chatStore.getMessages(agentId).some(row => row.id === 'old-captured-row')).toBe(true)
    })
  })

  it('restores cold scalar history through the real message handler', () => {
    withCaptureRoot(() => {
      const stores = storesForCapture(false)
      expect(stores.agentSessionStore.getInfo(agentId)).toEqual({})
      stores.agentSessionStore.beginReplay(agentId, 1n)
      const earlier = message({ type: 'assistant', total_cost_usd: 1, context_usage: { input_tokens: 10 } }, false)
      handleAgentMessage(agentId, earlier, stores, 'catchingUp', 1n)
      const later = message({ type: 'result', total_cost_usd: 2, context_usage: { input_tokens: 20 } }, false)
      handleAgentMessage(agentId, later, stores, 'catchingUp', 1n)
      handleAgentMessage(agentId, message({ type: 'plan_updated', plan_file_path: '/history-plan', plan_title: 'Historical title', update_agent_title: true }, false, MessageSource.LEAPMUX), stores, 'catchingUp', 1n)
      expect(stores.agentSessionStore.getInfo(agentId)).toMatchObject({ totalCostUsd: 2, contextUsage: { inputTokens: 20 }, planFilePath: '/history-plan' })
      expect(stores.metadata.get(agentId)?.title).not.toBe('Historical title')
      expect(stores.agentSessionStore.getProgress(agentId).thinkingTokens).toBe(17)
      expect(stores.chatStore.getToolProgress(agentId, identity)?.outputTail).toBe('current output')
    })
  })

  it.each([19n, 20n, 21n])('compares replay row %s with the live field sequence', (seq) => {
    withCaptureRoot(() => {
      const stores = storesForCapture()
      stores.agentSessionStore.beginReplay(agentId, 1n)
      const live = message({ type: 'assistant', total_cost_usd: 3 }, false)
      handleAgentMessage(agentId, live, stores, 'live')
      const history = message({ type: 'result', total_cost_usd: 2 }, false)
      history.seq = seq
      handleAgentMessage(agentId, history, stores, 'catchingUp', 1n)
      expect(stores.agentSessionStore.getInfo(agentId).totalCostUsd).toBe(seq > 20n ? 2 : 3)
    })
  })

  it('keeps an ephemeral zero and clear through history on the same request', () => {
    withCaptureRoot(() => {
      const stores = storesForCapture()
      stores.agentSessionStore.beginReplay(agentId, 1n)
      const ephemeral = message({ type: 'agent_session_info', info: { total_cost_usd: 0 } }, false)
      ephemeral.seq = -1n
      handleAgentMessage(agentId, ephemeral, stores, 'live')
      handleAgentMessage(agentId, message({ type: 'result', total_cost_usd: 9 }, false), stores, 'catchingUp', 1n)
      expect(stores.agentSessionStore.getInfo(agentId).totalCostUsd).toBe(0)
      const clear = message({ type: 'context_cleared' }, false, MessageSource.LEAPMUX)
      handleAgentMessage(agentId, clear, stores, 'live')
      handleAgentMessage(agentId, message({ type: 'assistant', total_cost_usd: 9, context_usage: { input_tokens: 10 } }, false), stores, 'catchingUp', 1n)
      expect(stores.agentSessionStore.getInfo(agentId).totalCostUsd).toBeUndefined()
      expect(stores.agentSessionStore.getInfo(agentId).contextUsage).toBeUndefined()
    })
  })

  it('retains transcript rows while a superseded replay changes no current state', () => {
    withCaptureRoot(() => {
      const stores = storesForCapture()
      stores.agentSessionStore.beginReplay(agentId, 1n)
      stores.agentSessionStore.beginReplay(agentId, 2n)
      handleAgentMessage(agentId, message({ type: 'result', total_cost_usd: 9 }, false), stores, 'catchingUp', 1n)
      expect(stores.agentSessionStore.getInfo(agentId).totalCostUsd).toBe(2)
      expect(stores.agentSessionStore.getProgress(agentId).thinkingTokens).toBe(17)
      expect(stores.chatStore.getToolProgress(agentId, identity)?.outputTail).toBe('current output')
      expect(stores.chatStore.getMessages(agentId).some(row => row.id === 'old-captured-row')).toBe(true)
    })
  })

  it.each(['live', 'catchingUp'] as const)('keeps validated supplemental usage after a divider arrives during %s', (phase) => {
    withCaptureRoot(() => {
      const stores = storesForCapture(false)
      stores.agentSessionStore.beginReplay(agentId, 1n)
      const native = { type: 'result', subtype: 'success', total_cost_usd: 9, context_usage: { input_tokens: 1000 } }
      const supplement = { metadata: { total_cost_usd: 0, context_usage: { input_tokens: 12, context_window: 100 } } }
      const row = message(native, false)
      row.supplementalContent = new TextEncoder().encode(JSON.stringify(supplement))
      row.supplementalContentCompression = ContentCompression.NONE
      handleAgentMessage(agentId, row, stores, phase, 1n)
      expect(stores.agentSessionStore.getInfo(agentId).totalCostUsd).toBe(0)
      expect(stores.agentSessionStore.getInfo(agentId).contextUsage).toMatchObject({ inputTokens: 12, contextWindow: 100 })
      expect(new TextDecoder().decode(row.content)).toBe(JSON.stringify(native))
      expect(new TextDecoder().decode(row.supplementalContent)).toBe(JSON.stringify(supplement))
      expect(stores.chatStore.getMessages(agentId)[0]?.id).toBe(row.id)
    })
  })
})
