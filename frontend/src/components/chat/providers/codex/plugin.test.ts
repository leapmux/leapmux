import type { AskQuestionState, Question } from '../../controls/types'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider, MessageRole } from '~/generated/leapmux/v1/agent_pb'
import { sendCodexDecision, sendCodexUserInputResponse, toRpcId } from '../../controls/CodexControlRequest'
import { providerFor } from '../registry'
import { input, model, option, optionGroup } from '../testUtils'

// Side-effect import to register the Codex plugin.
import './plugin'

describe('codex extractQuotableText', () => {
  const plugin = providerFor(AgentProvider.CODEX)!

  it('reads parent.item.text for assistant_text', () => {
    const parent = { item: { type: 'agentMessage', text: '  Hello  ' } }
    expect(plugin.extractQuotableText!({ kind: 'assistant_text' }, input(parent))).toBe('Hello')
  })

  it('reads parent.item.text for assistant_thinking', () => {
    const parent = { item: { type: 'reasoning', text: 'thinking...' } }
    expect(plugin.extractQuotableText!({ kind: 'assistant_thinking' }, input(parent))).toBe('thinking...')
  })

  it('reads parent.content string for user_content / plan_execution', () => {
    expect(plugin.extractQuotableText!({ kind: 'user_content' }, input({ content: 'hi' }))).toBe('hi')
    expect(plugin.extractQuotableText!({ kind: 'plan_execution' }, input({ content: 'plan' }))).toBe('plan')
  })

  it('returns null when item has no text', () => {
    const parent = { item: { type: 'agentMessage' } }
    expect(plugin.extractQuotableText!({ kind: 'assistant_text' }, input(parent))).toBeNull()
  })

  it('returns null for non-quotable categories', () => {
    expect(plugin.extractQuotableText!({ kind: 'hidden' }, input({ item: { type: 'agentMessage', text: 'x' } }))).toBeNull()
  })
})

describe('codex classify', () => {
  const plugin = providerFor(AgentProvider.CODEX)!

  it('exposes attachment capabilities', () => {
    expect(plugin.attachments).toEqual({
      text: true,
      image: true,
      pdf: false,
      binary: false,
    })
  })

  it('defaults to the "auto" effort sentinel so Codex picks its own default', () => {
    expect(plugin.defaultEffort).toBe('auto')
  })

  it('hides thread/started notifications', () => {
    const parent = {
      method: 'thread/started',
      params: {
        threadId: '019d0b79-3982-7bf2-b85c-890371421ade',
      },
    }
    const result = plugin.classify(input(parent))
    expect(result).toEqual({ kind: 'hidden' })
  })

  it('hides turn/started notifications', () => {
    const parent = {
      method: 'turn/started',
      params: {
        threadId: '019d0b79-3982-7bf2-b85c-890371421ade',
        turn: {
          id: 'turn_123',
        },
      },
    }
    const result = plugin.classify(input(parent))
    expect(result).toEqual({ kind: 'hidden' })
  })

  it('hides thread/status/changed notifications', () => {
    const parent = {
      method: 'thread/status/changed',
      params: {
        threadId: '019d0b79-3982-7bf2-b85c-890371421ade',
        status: {
          type: 'active',
          activeFlags: ['waitingOnApproval'],
        },
      },
    }
    const result = plugin.classify(input(parent))
    expect(result).toEqual({ kind: 'hidden' })
  })

  it('hides skills/changed notifications', () => {
    const parent = {
      method: 'skills/changed',
      params: {},
    }
    const result = plugin.classify(input(parent))
    expect(result).toEqual({ kind: 'hidden' })
  })

  it('hides remoteControl/status/changed notifications', () => {
    const parent = {
      method: 'remoteControl/status/changed',
      params: { status: 'disabled', environmentId: null },
    }
    const result = plugin.classify(input(parent))
    expect(result).toEqual({ kind: 'hidden' })
  })

  it.each([
    'hook/started',
    'hook/completed',
  ])('hides %s notifications', (method) => {
    const parent = {
      method,
      params: {
        threadId: 'thread-1',
        turnId: 'turn-1',
        run: { name: 'hook' },
      },
    }
    const result = plugin.classify(input(parent))
    expect(result).toEqual({ kind: 'hidden' })
  })

  it('classifies mixed wrappers when context_cleared follows a hidden Codex lifecycle event', () => {
    const wrapper = {
      old_seqs: [],
      messages: [
        { method: 'thread/started', params: { threadId: 'thread-1' } },
        { type: 'context_cleared' },
      ],
    }
    const result = plugin.classify(input(undefined, wrapper))
    expect(result).toEqual({ kind: 'notification_thread', messages: wrapper.messages })
  })

  it('classifies wrapped raw thread/compacted (Phase 4.3) as a notification thread', () => {
    const wrapper = {
      old_seqs: [],
      messages: [{ method: 'thread/compacted', params: { threadId: 't1', turnId: 'turn1' } }],
    }
    const result = plugin.classify(input(undefined, wrapper))
    expect(result.kind).toBe('notification_thread')
  })

  it('classifies wrapped raw item/started+contextCompaction (Phase 4.2) as a notification thread', () => {
    const wrapper = {
      old_seqs: [],
      messages: [{
        method: 'item/started',
        params: { item: { type: 'contextCompaction', id: 'compact-1' }, threadId: 't1', turnId: 'turn1' },
      }],
    }
    const result = plugin.classify(input(undefined, wrapper))
    expect(result.kind).toBe('notification_thread')
  })

  it('does NOT classify wrapped item/started for non-compaction items as a notification thread', () => {
    const wrapper = {
      old_seqs: [],
      messages: [{
        method: 'item/started',
        params: { item: { type: 'commandExecution', id: 'cmd-1' } },
      }],
    }
    // commandExecution is rendered through the assistant span flow, not the
    // notification thread. Wrapping it must not turn it into a notif thread.
    const result = plugin.classify(input(undefined, wrapper))
    expect(result.kind).not.toBe('notification_thread')
  })

  it('preserves wrapped skills and remote-control metadata in notification thread messages', () => {
    const wrapper = {
      old_seqs: [],
      messages: [
        { method: 'skills/changed', params: {} },
        { method: 'remoteControl/status/changed', params: { status: 'disabled', environmentId: null } },
      ],
    }
    const result = plugin.classify(input(undefined, wrapper))
    expect(result).toEqual({ kind: 'notification_thread', messages: wrapper.messages })
  })

  it('preserves hidden Codex metadata alongside visible notification thread entries', () => {
    const settingsChanged = { type: 'settings_changed', changes: { model: { old: 'a', new: 'b' } } }
    const wrapper = {
      old_seqs: [],
      messages: [
        { method: 'skills/changed', params: {} },
        settingsChanged,
        { method: 'remoteControl/status/changed', params: { status: 'disabled', environmentId: null } },
      ],
    }
    const result = plugin.classify(input(undefined, wrapper))
    expect(result).toEqual({ kind: 'notification_thread', messages: wrapper.messages })
  })

  it('classifies wrapped thread/tokenUsage/updated and thread/name/updated as a notification thread', () => {
    const wrapper = {
      old_seqs: [],
      messages: [
        { method: 'thread/name/updated', params: { threadId: 't1', name: 'Refactor auth' } },
        { method: 'thread/tokenUsage/updated', params: { threadId: 't1' } },
      ],
    }
    const result = plugin.classify(input(undefined, wrapper))
    expect(result.kind).toBe('notification_thread')
  })

  it('keeps high-usage rate limit notifications visible', () => {
    const parent = {
      method: 'account/rateLimits/updated',
      params: {
        rateLimits: {
          primary: {
            usedPercent: 85,
            windowMinutes: 300,
          },
        },
      },
    }
    const result = plugin.classify(input(parent))
    expect(result).toEqual({ kind: 'notification' })
  })

  it('classifies MCP startup starting notifications as visible', () => {
    const parent = {
      method: 'mcpServer/startupStatus/updated',
      params: { name: 'codex_apps', status: 'starting', error: null },
    }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'notification' })
  })

  it('classifies MCP startup terminal notifications as visible', () => {
    for (const status of ['ready', 'failed', 'cancelled']) {
      const parent = {
        method: 'mcpServer/startupStatus/updated',
        params: { name: 'codex_apps', status, error: status === 'failed' ? 'boom' : null },
      }
      expect(plugin.classify(input(parent))).toEqual({ kind: 'notification' })
    }
  })

  it('classifies compacting as notification', () => {
    const parent = { type: 'compacting' }
    const result = plugin.classify(input(parent))
    expect(result).toEqual({ kind: 'notification' })
  })

  it('classifies compact_boundary system messages as notification', () => {
    const parent = { type: 'system', subtype: 'compact_boundary' }
    const result = plugin.classify(input(parent))
    expect(result).toEqual({ kind: 'notification' })
  })

  it('classifies turn/plan/updated as a Codex tool-use message', () => {
    const parent = {
      method: 'turn/plan/updated',
      params: {
        threadId: 'thread-1',
        turnId: 'turn-1',
        explanation: null,
        plan: [
          { step: 'Inspect messages', status: 'inProgress' },
          { step: 'Update renderer', status: 'pending' },
        ],
      },
    }
    const result = plugin.classify(input(parent))
    expect(result).toEqual({ kind: 'tool_use', toolName: 'turnPlan', toolUse: parent, content: [] })
  })

  it('classifies webSearch items as Codex tool-use messages', () => {
    const parent = {
      item: {
        type: 'webSearch',
        id: 'ws-1',
        query: 'https://example.com',
        action: { type: 'openPage', url: 'https://example.com' },
      },
      threadId: 'thread-1',
      turnId: 'turn-1',
    }
    const result = plugin.classify(input(parent))
    expect(result).toEqual({ kind: 'tool_use', toolName: 'webSearch', toolUse: parent.item, content: [] })
  })

  it('hides webSearch openPage items with null url', () => {
    const parent = {
      item: {
        type: 'webSearch',
        id: 'ws-2',
        query: '',
        action: { type: 'openPage', url: null },
      },
      threadId: 'thread-1',
      turnId: 'turn-1',
    }
    const result = plugin.classify(input(parent))
    expect(result).toEqual({ kind: 'hidden' })
  })

  it('hides thread/tokenUsage/updated notifications', () => {
    const parent = {
      method: 'thread/tokenUsage/updated',
      params: {
        threadId: 'thread-1',
        turnId: 'turn-1',
        tokenUsage: {
          total: { totalTokens: 200, inputTokens: 100, cachedInputTokens: 25, outputTokens: 50, reasoningOutputTokens: 9 },
          last: { totalTokens: 23, inputTokens: 10, cachedInputTokens: 5, outputTokens: 7, reasoningOutputTokens: 1 },
          modelContextWindow: 4096,
        },
      },
    }
    const result = plugin.classify(input(parent))
    expect(result).toEqual({ kind: 'hidden' })
  })

  it('hides notification threads containing only hidden Codex notifications', () => {
    const wrapper = {
      old_seqs: [],
      messages: [
        {
          method: 'thread/tokenUsage/updated',
          params: {
            threadId: 'thread-1',
            turnId: 'turn-1',
            tokenUsage: {
              total: { totalTokens: 200, inputTokens: 100, cachedInputTokens: 25, outputTokens: 50, reasoningOutputTokens: 9 },
              last: { totalTokens: 23, inputTokens: 10, cachedInputTokens: 5, outputTokens: 7, reasoningOutputTokens: 1 },
              modelContextWindow: 4096,
            },
          },
        },
        {
          method: 'account/rateLimits/updated',
          params: {
            rateLimits: {
              primary: { usedPercent: 34, windowMinutes: 300 },
              secondary: { usedPercent: 10, windowMinutes: 10080 },
            },
          },
        },
      ],
    }
    expect(plugin.classify(input(undefined, wrapper))).toEqual({ kind: 'hidden' })
  })

  it('hides assistant interrupt echo messages with top-level string content', () => {
    const parent = {
      role: 'assistant',
      content: '{"jsonrpc":"2.0","id":1001,"method":"turn/interrupt","params":{"threadId":"thread-1","turnId":"turn-1"}}',
    }
    const result = plugin.classify(input(parent))
    expect(result).toEqual({ kind: 'hidden' })
  })

  it('hides assistant interrupt echo messages with assistant text blocks', () => {
    const parent = {
      role: 'assistant',
      type: 'assistant',
      message: {
        content: [
          {
            type: 'text',
            text: '{"jsonrpc":"2.0","id":1001,"method":"turn/interrupt","params":{"threadId":"thread-1","turnId":"turn-1"}}',
          },
        ],
      },
    }
    const result = plugin.classify(input(parent))
    expect(result).toEqual({ kind: 'hidden' })
  })

  it('hides plain JSON-RPC response envelopes', () => {
    const parent = {
      id: 1001,
      result: {},
    }
    const result = plugin.classify(input(parent))
    expect(result).toEqual({ kind: 'hidden' })
  })
})

describe('codex result divider', () => {
  const plugin = providerFor(AgentProvider.CODEX)!

  it('classifies turn/completed as result_divider', () => {
    const parent = {
      method: 'turn/completed',
      params: {
        threadId: 'thread-1',
        turnId: 'turn-1',
        turn: { id: 'turn-1', status: 'completed' },
      },
      turn: { id: 'turn-1', status: 'completed' },
    }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'result_divider' })
  })

  it('hides synthetic Codex turn failed notifications', () => {
    const parent = {
      type: 'agent_error',
      error: 'Codex turn failed',
    }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('hides notification threads containing only synthetic Codex turn failed notifications', () => {
    const wrapper = {
      old_seqs: [],
      messages: [
        { type: 'agent_error', error: 'Codex turn failed' },
      ],
    }
    expect(plugin.classify(input(undefined, wrapper))).toEqual({ kind: 'hidden' })
  })

  it('renders result_divider via renderMessage', () => {
    const parsed = {
      turn: { id: 'turn-1', status: 'completed' },
    }
    const result = plugin.renderMessage!({ kind: 'result_divider' }, parsed, MessageRole.TURN_END)
    expect(result).not.toBeNull()
  })
})

describe('codex isAskUserQuestion', () => {
  const plugin = providerFor(AgentProvider.CODEX)!

  it('returns true for requestUserInput method', () => {
    const payload = {
      method: 'item/tool/requestUserInput',
      params: { questions: [] },
    }
    expect(plugin.isAskUserQuestion!(payload)).toBe(true)
  })

  it('returns false for approval methods', () => {
    expect(plugin.isAskUserQuestion!({
      method: 'item/commandExecution/requestApproval',
    })).toBe(false)
  })

  it('returns false for payloads without method', () => {
    expect(plugin.isAskUserQuestion!({
      request: { tool_name: 'AskUserQuestion' },
    })).toBe(false)
  })
})

describe('toRpcId', () => {
  it('converts numeric string to number', () => {
    expect(toRpcId('42')).toBe(42)
  })

  it('preserves non-numeric string', () => {
    expect(toRpcId('abc')).toBe('abc')
  })

  it('converts zero', () => {
    expect(toRpcId('0')).toBe(0)
  })
})

describe('sendCodexDecision', () => {
  function decode(bytes: Uint8Array): Record<string, unknown> {
    return JSON.parse(new TextDecoder().decode(bytes))
  }

  it('sends accept decision with numeric id', async () => {
    let captured: Uint8Array | undefined
    const onRespond = vi.fn(async (_id: string, content: Uint8Array) => {
      captured = content
    })

    await sendCodexDecision('agent1', onRespond, '42', 'accept')

    expect(onRespond).toHaveBeenCalledOnce()
    const parsed = decode(captured!)
    expect(parsed).toMatchObject({
      jsonrpc: '2.0',
      id: 42,
      result: { decision: 'accept' },
    })
  })

  it('sends decline decision', async () => {
    let captured: Uint8Array | undefined
    const onRespond = vi.fn(async (_id: string, content: Uint8Array) => {
      captured = content
    })

    await sendCodexDecision('agent1', onRespond, '7', 'decline')

    const parsed = decode(captured!)
    expect(parsed).toMatchObject({
      jsonrpc: '2.0',
      id: 7,
      result: { decision: 'decline' },
    })
  })

  it('sends object decision', async () => {
    let captured: Uint8Array | undefined
    const onRespond = vi.fn(async (_id: string, content: Uint8Array) => {
      captured = content
    })
    const decision = { acceptWithExecpolicyAmendment: { execpolicy_amendment: ['touch'] } }

    await sendCodexDecision('agent1', onRespond, '9', decision)

    const parsed = decode(captured!)
    expect(parsed).toMatchObject({
      jsonrpc: '2.0',
      id: 9,
      result: { decision },
    })
  })

  it('preserves non-numeric request id', async () => {
    let captured: Uint8Array | undefined
    const onRespond = vi.fn(async (_id: string, content: Uint8Array) => {
      captured = content
    })

    await sendCodexDecision('agent1', onRespond, 'abc', 'accept')

    const parsed = decode(captured!)
    expect(parsed).toMatchObject({
      jsonrpc: '2.0',
      id: 'abc',
      result: { decision: 'accept' },
    })
  })
})

describe('sendCodexUserInputResponse', () => {
  function decode(bytes: Uint8Array): Record<string, unknown> {
    return JSON.parse(new TextDecoder().decode(bytes))
  }

  function makeAskState(overrides: {
    selections?: Record<number, string[]>
    customTexts?: Record<number, string>
  } = {}): AskQuestionState {
    const [selections, setSelections] = createSignal<Record<number, string[]>>(overrides.selections ?? {})
    const [customTexts, setCustomTexts] = createSignal<Record<number, string>>(overrides.customTexts ?? {})
    const [currentPage, setCurrentPage] = createSignal(0)
    return { selections, setSelections, customTexts, setCustomTexts, currentPage, setCurrentPage }
  }

  it('sends answers using question id as key', async () => {
    let captured: Uint8Array | undefined
    const onRespond = vi.fn(async (_id: string, content: Uint8Array) => {
      captured = content
    })

    const questions: Question[] = [
      { id: 'q1', question: 'Pick one', header: 'Header1', options: [{ label: 'A' }, { label: 'B' }] },
    ]
    const state = makeAskState({ selections: { 0: ['A'] } })

    await sendCodexUserInputResponse('agent1', onRespond, '42', questions, state)

    const parsed = decode(captured!)
    expect(parsed).toMatchObject({
      jsonrpc: '2.0',
      id: 42,
      result: {
        answers: {
          q1: { answers: ['A'] },
        },
      },
    })
  })

  it('falls back to header as key when id is missing', async () => {
    let captured: Uint8Array | undefined
    const onRespond = vi.fn(async (_id: string, content: Uint8Array) => {
      captured = content
    })

    const questions: Question[] = [
      { question: 'Pick one', header: 'MyHeader', options: [{ label: 'X' }] },
    ]
    const state = makeAskState({ selections: { 0: ['X'] } })

    await sendCodexUserInputResponse('agent1', onRespond, '5', questions, state)

    const parsed = decode(captured!)
    expect(parsed).toMatchObject({
      jsonrpc: '2.0',
      id: 5,
      result: {
        answers: {
          MyHeader: { answers: ['X'] },
        },
      },
    })
  })

  it('sends custom text as a Codex user_note when no option is selected', async () => {
    let captured: Uint8Array | undefined
    const onRespond = vi.fn(async (_id: string, content: Uint8Array) => {
      captured = content
    })

    const questions: Question[] = [
      { id: 'q1', question: 'Custom input', options: [] },
    ]
    const state = makeAskState({ customTexts: { 0: 'my custom answer' } })

    await sendCodexUserInputResponse('agent1', onRespond, '10', questions, state)

    const parsed = decode(captured!)
    expect(parsed).toMatchObject({
      jsonrpc: '2.0',
      id: 10,
      result: {
        answers: {
          q1: { answers: ['user_note: my custom answer'] },
        },
      },
    })
  })

  it('sends multi-select values as separate answer entries', async () => {
    let captured: Uint8Array | undefined
    const onRespond = vi.fn(async (_id: string, content: Uint8Array) => {
      captured = content
    })

    const questions: Question[] = [
      { id: 'q1', question: 'Pick multiple', options: [{ label: 'A' }, { label: 'B' }, { label: 'C' }], multiSelect: true },
    ]
    const state = makeAskState({ selections: { 0: ['A', 'C'] } })

    await sendCodexUserInputResponse('agent1', onRespond, '11', questions, state)

    const parsed = decode(captured!)
    expect(parsed).toMatchObject({
      jsonrpc: '2.0',
      id: 11,
      result: {
        answers: {
          q1: { answers: ['A', 'C'] },
        },
      },
    })
  })

  it('preserves selected options and appends custom text as a Codex user_note', async () => {
    let captured: Uint8Array | undefined
    const onRespond = vi.fn(async (_id: string, content: Uint8Array) => {
      captured = content
    })

    const questions: Question[] = [
      { id: 'q1', question: 'Pick one', options: [{ label: 'A' }, { label: 'B' }] },
    ]
    const state = makeAskState({ selections: { 0: ['B'] }, customTexts: { 0: 'note for B' } })

    await sendCodexUserInputResponse('agent1', onRespond, '13', questions, state)

    const parsed = decode(captured!)
    expect(parsed).toMatchObject({
      jsonrpc: '2.0',
      id: 13,
      result: {
        answers: {
          q1: { answers: ['B', 'user_note: note for B'] },
        },
      },
    })
  })

  it('formats Codex Other/custom text like the native TUI', async () => {
    let captured: Uint8Array | undefined
    const onRespond = vi.fn(async (_id: string, content: Uint8Array) => {
      captured = content
    })

    const questions: Question[] = [
      {
        id: 'q1',
        question: 'Pick one',
        options: [{ label: 'A' }, { label: 'B' }],
        isOther: true,
      } as unknown as Question,
    ]
    const state = makeAskState({ customTexts: { 0: 'my custom answer' } })

    await sendCodexUserInputResponse('agent1', onRespond, '14', questions, state)

    const parsed = decode(captured!)
    expect(parsed).toMatchObject({
      jsonrpc: '2.0',
      id: 14,
      result: {
        answers: {
          q1: { answers: ['None of the above', 'user_note: my custom answer'] },
        },
      },
    })
  })

  it('includes unanswered questions with empty answer lists like the native TUI', async () => {
    let captured: Uint8Array | undefined
    const onRespond = vi.fn(async (_id: string, content: Uint8Array) => {
      captured = content
    })

    const questions: Question[] = [
      { id: 'q1', question: 'First', options: [{ label: 'A' }] },
      { id: 'q2', question: 'Second', options: [{ label: 'B' }] },
    ]
    const state = makeAskState({ selections: { 0: ['A'] } })

    await sendCodexUserInputResponse('agent1', onRespond, '12', questions, state)

    const parsed = decode(captured!)
    expect(parsed).toMatchObject({
      result: {
        answers: {
          q1: { answers: ['A'] },
          q2: { answers: [] },
        },
      },
    })
  })
})

describe('codex settings panel', () => {
  const plugin = providerFor(AgentProvider.CODEX)!

  const baseModels = [
    model('gpt-5.4', 'GPT-5.4', {
      isDefault: true,
      supportedEfforts: [
        { id: 'auto', name: 'Auto', description: '' },
        { id: 'high', name: 'High', description: '' },
      ],
    }),
  ]

  const baseOptionGroups = [
    optionGroup('service_tier', 'Fast Mode', [
      option('default', 'Default', { isDefault: true }),
      option('fast', 'Fast'),
    ]),
    optionGroup('collaboration_mode', 'Workflow', [
      option('default', 'Suggest & Approve', { isDefault: true }),
      option('plan', 'Plan Mode'),
    ]),
    optionGroup('permissionMode', 'Approval Policy', [
      option('on-request', 'On Request', { isDefault: true }),
      option('never', 'Never'),
    ]),
  ]

  it('renders the Fast Mode group and toggles to Fast via the unified onChange dispatcher', async () => {
    const onChange = vi.fn()
    render(() => plugin.SettingsPanel!({
      model: 'gpt-5.4',
      effort: 'auto',
      permissionMode: 'on-request',
      availableModels: baseModels,
      availableOptionGroups: baseOptionGroups,
      onChange,
    }))

    expect(screen.getByText('Fast Mode')).toBeInTheDocument()
    expect(screen.getByTestId('codex-service-tier-default')).toBeInTheDocument()
    expect(screen.getByTestId('codex-service-tier-fast')).toBeInTheDocument()

    await fireEvent.click(screen.getByDisplayValue('fast'))
    expect(onChange).toHaveBeenCalledWith({ kind: 'optionGroup', key: 'service_tier', value: 'fast' })
  })

  it('switches collaboration mode through onChange (sticky plan mode entry)', async () => {
    const onChange = vi.fn()
    render(() => plugin.SettingsPanel!({
      model: 'gpt-5.4',
      effort: 'auto',
      permissionMode: 'on-request',
      availableModels: baseModels,
      availableOptionGroups: baseOptionGroups,
      onChange,
    }))

    expect(screen.getByText('Workflow')).toBeInTheDocument()
    await fireEvent.click(screen.getByDisplayValue('plan'))
    expect(onChange).toHaveBeenCalledWith({ kind: 'optionGroup', key: 'collaboration_mode', value: 'plan' })
  })

  it('renders Fast Mode as the first fieldset in the left column', () => {
    const { container } = render(() => plugin.SettingsPanel!({
      model: 'gpt-5.4',
      effort: 'auto',
      permissionMode: 'on-request',
      availableModels: baseModels,
      availableOptionGroups: baseOptionGroups,
    }))

    // The left column orders fieldsets Fast Mode → Reasoning Effort → Models,
    // so the first group label rendered must be "Fast Mode".
    const labels = container.querySelectorAll('div[role="group"] > div')
    const firstLabel = labels[0]?.textContent
    expect(firstLabel).toBe('Fast Mode')
  })

  it('updates permissionMode through the unified onChange dispatcher', async () => {
    const onChange = vi.fn()
    render(() => plugin.SettingsPanel!({
      model: 'gpt-5.4',
      effort: 'auto',
      permissionMode: 'on-request',
      availableModels: baseModels,
      availableOptionGroups: baseOptionGroups,
      onChange,
    }))

    await fireEvent.click(screen.getByDisplayValue('never'))
    expect(onChange).toHaveBeenCalledWith({ kind: 'permissionMode', value: 'never' })
  })

  it('trigger label includes the model display name', () => {
    const { container } = render(() => plugin.settingsTriggerLabel!({
      model: 'gpt-5.4',
      effort: 'auto',
      permissionMode: 'on-request',
      availableModels: baseModels,
      availableOptionGroups: baseOptionGroups,
    }))

    expect(container.textContent).toContain('GPT-5.4')
  })

  it('falls back to the default model display name when no model prop is set', () => {
    const { container } = render(() => plugin.settingsTriggerLabel!({
      effort: 'auto',
      permissionMode: 'on-request',
      availableModels: baseModels,
      availableOptionGroups: baseOptionGroups,
    }))

    // Default model is the one with isDefault=true.
    expect(container.textContent).toContain('GPT-5.4')
  })
})
