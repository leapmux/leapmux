import type { ControlQuestion } from '../../model/question'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it, vi } from 'vitest'
import { CODEX_OPTION, CODEX_OPTION_DEFAULT } from '~/generated/contracts/codex-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { renderDivider } from '~/test-support/messageRenderProbes'
import { providerQuotableText } from '~/test-support/toolCallFixture'
import { createControlAnswerState } from '../../controls/types'
import { providerFor } from '../registry'
import { input } from '../testUtils'

import { sendCodexDecision, sendCodexUserInputResponse } from './controlResponse'
// Side-effect import to register the Codex plugin.
import './plugin'

describe('codex provider capabilities', () => {
  const plugin = providerFor(AgentProvider.CODEX)!

  it('seeds the default collaboration mode as a provider option on a new agent', () => {
    expect(plugin?.configuration?.defaultProviderOptions).toEqual({ [CODEX_OPTION.CollaborationMode]: CODEX_OPTION_DEFAULT.CollaborationMode })
  })

  it('preserves an option selection alongside the free-text note', () => {
    expect(plugin?.controls?.preservesSelectionNotes).toBe(true)
  })

  it('exposes attachment capabilities', () => {
    expect(plugin?.configuration?.attachments).toEqual({
      text: true,
      image: true,
      pdf: false,
      binary: false,
    })
  })
})

describe('codex quotable text', () => {
  it('reads parent.item.text for assistant_text', () => {
    const parent = { item: { type: 'agentMessage', text: '  Hello  ' } }
    expect(providerQuotableText(AgentProvider.CODEX, parent, { category: { kind: 'assistant_text' } })).toBe('Hello')
  })

  it('reads parent.item.text for assistant_thinking', () => {
    const parent = { item: { type: 'reasoning', text: 'thinking...' } }
    expect(providerQuotableText(AgentProvider.CODEX, parent, { category: { kind: 'assistant_thinking' } })).toBe('thinking...')
  })

  it('reads parent.content string for user_content / plan_execution', () => {
    expect(providerQuotableText(AgentProvider.CODEX, { content: 'hi' }, { category: { kind: 'user_content' } })).toBe('hi')
    expect(providerQuotableText(AgentProvider.CODEX, { content: 'plan' }, { category: { kind: 'plan_execution' } })).toBe('plan')
  })

  it('returns null when item has no text', () => {
    const parent = { item: { type: 'agentMessage' } }
    expect(providerQuotableText(AgentProvider.CODEX, parent, { category: { kind: 'assistant_text' } })).toBeNull()
  })

  it('returns null for non-quotable categories', () => {
    expect(providerQuotableText(AgentProvider.CODEX, { item: { type: 'agentMessage', text: 'x' } }, { category: { kind: 'hidden' } })).toBeNull()
  })
})

describe('codex result divider', () => {
  const plugin = providerFor(AgentProvider.CODEX)!

  // Every provider states a turn end in one shared vocabulary, so the runtime's own
  // status word never reaches the label on its own. Codex said "Turn completed" where
  // the Agent Client Protocol providers said "Turn ended".
  it('maps a completed turn to the shared turn-end label', () => {
    expect(plugin?.transcript.extractDivider!({ turn: { id: 'turn-1', status: 'completed' } }))
      .toEqual({ label: 'Turn ended' })
  })

  it('maps the statuses that mean the reader stopped the turn', () => {
    for (const status of ['interrupted', 'cancelled', 'aborted']) {
      expect(plugin?.transcript.extractDivider!({ turn: { id: 'turn-1', status } }), status)
        .toEqual({ label: 'Turn interrupted' })
    }
  })

  // A word this build does not know still reads as a turn end, with the word itself
  // kept so nothing the runtime reported is lost.
  it('qualifies the turn end with a status this build does not know', () => {
    expect(plugin?.transcript.extractDivider!({ turn: { id: 'turn-1', status: 'compacted' } }))
      .toEqual({ label: 'Turn ended (compacted)' })
  })

  it('maps a failed turn to a danger divider with the error inline', () => {
    expect(plugin?.transcript.extractDivider!({ turn: { status: 'failed', error: { message: 'Boom', additionalDetails: 'timeout' } } }))
      .toEqual({ label: 'Turn failed — Boom', isError: true, detail: 'timeout' })
  })

  // A failure the runtime reports with no error object at all still reads as one.
  it('marks a failed turn that carries no error object', () => {
    expect(plugin?.transcript.extractDivider!({ turn: { status: 'failed' } }))
      .toEqual({ label: 'Turn failed', isError: true })
  })

  it('falls back to "Unknown error" for a failed turn whose error.message is empty', () => {
    // An explicit empty-string message is a present string, so pickString's
    // missing-key fallback does not apply -- guard with `|| 'Unknown error'` so
    // the divider never renders a label-less red row.
    expect(plugin?.transcript.extractDivider!({ turn: { status: 'failed', error: { message: '' } } }))
      .toEqual({ label: 'Turn failed — Unknown error', isError: true })
  })

  it('returns null when the turn carries no status', () => {
    expect(plugin?.transcript.extractDivider!({ turn: {} })).toBeNull()
  })

  it('renders a failed turn as a danger divider through the shared renderer end-to-end', () => {
    // MessageBubble routes result_divider through the shared row extraction, which draws
    // the shared ResultDivider with the inline danger color for a failed turn.
    const { text, isError } = renderDivider(
      { turn: { status: 'failed', error: { message: 'Boom', additionalDetails: 'timeout' } } },
      AgentProvider.CODEX,
    )
    expect(text).toBe('Turn failed — Boomtimeout')
    expect(isError).toBe(true)
  })
})

describe('codex isAskUserQuestion', () => {
  const plugin = providerFor(AgentProvider.CODEX)!

  it('returns true for requestUserInput method', () => {
    const payload = {
      method: 'item/tool/requestUserInput',
      params: { questions: [] },
    }
    expect(plugin?.controls?.askUserQuestion!.isRequest(payload)).toBe(true)
  })

  it('returns false for approval methods', () => {
    expect(plugin?.controls?.askUserQuestion!.isRequest({
      method: 'item/commandExecution/requestApproval',
    })).toBe(false)
  })

  it('returns false for payloads without method', () => {
    expect(plugin?.controls?.askUserQuestion!.isRequest({
      request: { tool_name: 'AskUserQuestion' },
    })).toBe(false)
  })

  it('reads each question and its options', () => {
    const payload = {
      method: 'item/tool/requestUserInput',
      params: { questions: [{ question: 'Which one?', header: 'Pick', options: [{ label: 'A', description: 'first' }] }] },
    }
    expect(plugin?.controls?.askUserQuestion!.extractQuestions(payload)).toEqual([
      { question: 'Which one?', header: 'Pick', options: [{ label: 'A', description: 'first' }] },
    ])
  })

  // An `Array.isArray` on the OUTER array says nothing about the elements, and the
  // control surface dereferences `question` and hands `options` to a `<For>` -- so a
  // null or a bare string among them threw the whole banner away.
  it('drops an element the control surface cannot draw', () => {
    const payload = {
      method: 'item/tool/requestUserInput',
      params: { questions: [null, 'plain text', 7, { question: 'Which one?', options: [] }] },
    }
    expect(plugin?.controls?.askUserQuestion!.extractQuestions(payload)).toEqual([{ question: 'Which one?', options: [] }])
  })

  it('answers an empty list for a payload that states no questions', () => {
    expect(plugin?.controls?.askUserQuestion!.extractQuestions({ method: 'item/tool/requestUserInput' })).toEqual([])
    expect(plugin?.controls?.askUserQuestion!.extractQuestions({ method: 'item/tool/requestUserInput', params: { questions: 'nope' } })).toEqual([])
  })
})

describe('sendCodexDecision', () => {
  function decode(bytes: Uint8Array): Record<string, unknown> {
    return JSON.parse(new TextDecoder().decode(bytes))
  }

  it('sends accept decision with the unchanged worker request ID', async () => {
    let captured: Uint8Array | undefined
    const onRespond = vi.fn(async (content: Uint8Array) => {
      captured = content
    })

    await sendCodexDecision(onRespond, '42', 'accept')

    expect(onRespond).toHaveBeenCalledOnce()
    const parsed = decode(captured!)
    expect(parsed).toMatchObject({
      jsonrpc: '2.0',
      id: '42',
      result: { decision: 'accept' },
    })
  })

  it('sends decline decision', async () => {
    let captured: Uint8Array | undefined
    const onRespond = vi.fn(async (content: Uint8Array) => {
      captured = content
    })

    await sendCodexDecision(onRespond, '7', 'decline')

    const parsed = decode(captured!)
    expect(parsed).toMatchObject({
      jsonrpc: '2.0',
      id: '7',
      result: { decision: 'decline' },
    })
  })

  it('sends object decision', async () => {
    let captured: Uint8Array | undefined
    const onRespond = vi.fn(async (content: Uint8Array) => {
      captured = content
    })
    const decision = { acceptWithExecpolicyAmendment: { execpolicy_amendment: ['touch'] } }

    await sendCodexDecision(onRespond, '9', decision)

    const parsed = decode(captured!)
    expect(parsed).toMatchObject({
      jsonrpc: '2.0',
      id: '9',
      result: { decision },
    })
  })

  it('preserves non-numeric request id', async () => {
    let captured: Uint8Array | undefined
    const onRespond = vi.fn(async (content: Uint8Array) => {
      captured = content
    })

    await sendCodexDecision(onRespond, 'abc', 'accept')

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

  it('sends answers using question id as key', async () => {
    let captured: Uint8Array | undefined
    const onRespond = vi.fn(async (content: Uint8Array) => {
      captured = content
    })

    const questions: ControlQuestion[] = [
      { id: 'q1', question: 'Pick one', header: 'Header1', options: [{ label: 'A' }, { label: 'B' }] },
    ]
    const state = createControlAnswerState({ selections: { 0: ['A'] } })

    await sendCodexUserInputResponse(onRespond, '42', questions, state)

    const parsed = decode(captured!)
    expect(parsed).toMatchObject({
      jsonrpc: '2.0',
      id: '42',
      result: {
        answers: {
          q1: { answers: ['A'] },
        },
      },
    })
  })

  it('falls back to header as key when id is missing', async () => {
    let captured: Uint8Array | undefined
    const onRespond = vi.fn(async (content: Uint8Array) => {
      captured = content
    })

    const questions: ControlQuestion[] = [
      { question: 'Pick one', header: 'MyHeader', options: [{ label: 'X' }] },
    ]
    const state = createControlAnswerState({ selections: { 0: ['X'] } })

    await sendCodexUserInputResponse(onRespond, '5', questions, state)

    const parsed = decode(captured!)
    expect(parsed).toMatchObject({
      jsonrpc: '2.0',
      id: '5',
      result: {
        answers: {
          MyHeader: { answers: ['X'] },
        },
      },
    })
  })

  it('sends custom text as a Codex user_note when no option is selected', async () => {
    let captured: Uint8Array | undefined
    const onRespond = vi.fn(async (content: Uint8Array) => {
      captured = content
    })

    const questions: ControlQuestion[] = [
      { id: 'q1', question: 'Custom input', options: [] },
    ]
    const state = createControlAnswerState({ customTexts: { 0: 'my custom answer' } })

    await sendCodexUserInputResponse(onRespond, '10', questions, state)

    const parsed = decode(captured!)
    expect(parsed).toMatchObject({
      jsonrpc: '2.0',
      id: '10',
      result: {
        answers: {
          q1: { answers: ['user_note: my custom answer'] },
        },
      },
    })
  })

  it('sends multi-select values as separate answer entries', async () => {
    let captured: Uint8Array | undefined
    const onRespond = vi.fn(async (content: Uint8Array) => {
      captured = content
    })

    const questions: ControlQuestion[] = [
      { id: 'q1', question: 'Pick multiple', options: [{ label: 'A' }, { label: 'B' }, { label: 'C' }], multiSelect: true },
    ]
    const state = createControlAnswerState({ selections: { 0: ['A', 'C'] } })

    await sendCodexUserInputResponse(onRespond, '11', questions, state)

    const parsed = decode(captured!)
    expect(parsed).toMatchObject({
      jsonrpc: '2.0',
      id: '11',
      result: {
        answers: {
          q1: { answers: ['A', 'C'] },
        },
      },
    })
  })

  it('preserves selected options and appends custom text as a Codex user_note', async () => {
    let captured: Uint8Array | undefined
    const onRespond = vi.fn(async (content: Uint8Array) => {
      captured = content
    })

    const questions: ControlQuestion[] = [
      { id: 'q1', question: 'Pick one', options: [{ label: 'A' }, { label: 'B' }] },
    ]
    const state = createControlAnswerState({ selections: { 0: ['B'] }, customTexts: { 0: 'note for B' } })

    await sendCodexUserInputResponse(onRespond, '13', questions, state)

    const parsed = decode(captured!)
    expect(parsed).toMatchObject({
      jsonrpc: '2.0',
      id: '13',
      result: {
        answers: {
          q1: { answers: ['B', 'user_note: note for B'] },
        },
      },
    })
  })

  it('formats Codex Other/custom text like the native TUI', async () => {
    let captured: Uint8Array | undefined
    const onRespond = vi.fn(async (content: Uint8Array) => {
      captured = content
    })

    const questions: ControlQuestion[] = [
      {
        id: 'q1',
        question: 'Pick one',
        options: [{ label: 'A' }, { label: 'B' }],
        isOther: true,
      } as unknown as ControlQuestion,
    ]
    const state = createControlAnswerState({ customTexts: { 0: 'my custom answer' } })

    await sendCodexUserInputResponse(onRespond, '14', questions, state)

    const parsed = decode(captured!)
    expect(parsed).toMatchObject({
      jsonrpc: '2.0',
      id: '14',
      result: {
        answers: {
          q1: { answers: ['None of the above', 'user_note: my custom answer'] },
        },
      },
    })
  })

  it('includes unanswered questions with empty answer lists like the native TUI', async () => {
    let captured: Uint8Array | undefined
    const onRespond = vi.fn(async (content: Uint8Array) => {
      captured = content
    })

    const questions: ControlQuestion[] = [
      { id: 'q1', question: 'First', options: [{ label: 'A' }] },
      { id: 'q2', question: 'Second', options: [{ label: 'B' }] },
    ]
    const state = createControlAnswerState({ selections: { 0: ['A'] } })

    await sendCodexUserInputResponse(onRespond, '12', questions, state)

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

describe('codex settings config', () => {
  // The provider-specific settings panel was replaced by the composer's shared
  // settings surface (the status-bar chips and the `[+]` menu); the provider now
  // only declares the configuration those render. These assertions cover the declarative shape that
  // used to be exercised through the deleted panel/trigger-label renderers.
  const plugin = providerFor(AgentProvider.CODEX)!

  it('wires plan mode to the collaboration_mode group', () => {
    expect(plugin?.configuration?.planMode).toMatchObject({
      groupKey: 'collaboration_mode',
      planValue: 'plan',
      defaultValue: CODEX_OPTION_DEFAULT.CollaborationMode,
    })
  })

  it('renders the collaboration_mode "Workflow" group as the trigger mode segment', () => {
    // Not the approval-policy permissionMode -- Codex's mode axis is the Workflow group.
    expect(plugin?.configuration?.triggerModeGroupKey).toBe(CODEX_OPTION.CollaborationMode)
  })

  it('reads the current collaboration mode from optionValues, defaulting when unset', () => {
    expect(plugin?.configuration?.planMode!.currentMode({ optionValues: { [CODEX_OPTION.CollaborationMode]: 'plan' } })).toBe('plan')
    expect(plugin?.configuration?.planMode!.currentMode({})).toBe(CODEX_OPTION_DEFAULT.CollaborationMode)
  })

  it('declares one complete bypass permission preset', () => {
    expect(plugin?.controls?.permissionPresets).toEqual({
      bypass: { sets: {
        network_access: 'enabled',
        sandbox_policy: 'danger-full-access',
        permissionMode: 'never',
      } },
    })
  })
})

describe('codex control response builder', () => {
  const plugin = providerFor(AgentProvider.CODEX)!

  it('uses an offered cancel decision for typed feedback', () => {
    expect(plugin?.controls?.buildControlResponse!({
      method: 'item/commandExecution/requestApproval',
      params: { availableDecisions: ['accept', 'cancel'] },
    }, 'do something else', '7')).toEqual({
      jsonrpc: '2.0',
      id: '7',
      result: { decision: 'cancel' },
    })
  })

  it('uses an empty grant to reject a permission request', () => {
    expect(plugin?.controls?.buildControlResponse!({
      method: 'item/permissions/requestApproval',
      params: { permissions: { network: { enabled: true } } },
    }, 'do something else', '7')).toEqual({
      jsonrpc: '2.0',
      id: '7',
      result: { permissions: {}, scope: 'turn' },
    })
  })
})

// Build a plain ParsedMessageContent whose inner message is `inner`; the
// session-metadata hooks read `parsed.parentObject` (getInnerMessage falls back
// to topLevel), so both point at the same object.
function parsed(inner: Record<string, unknown>): ParsedMessageContent {
  return { rawText: '', topLevel: inner, parentObject: inner, wrapper: null }
}

describe('codex contextUsageFromMessage', () => {
  const plugin = providerFor(AgentProvider.CODEX)!

  it('extracts context usage from a thread/tokenUsage/updated notification (params.tokenUsage.last)', () => {
    const msg = parsed({
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
    })
    expect(plugin?.session?.contextUsageFromMessage!(msg)).toEqual({
      inputTokens: 5,
      cacheCreationInputTokens: 0,
      cacheReadInputTokens: 5,
      contextWindow: 4096,
    })
  })

  it('returns null for an unrelated method', () => {
    expect(plugin?.session?.contextUsageFromMessage!(parsed({ method: 'turn/completed', params: {} }))).toBeNull()
  })
})

describe('codex rateLimitsFromMessage', () => {
  const plugin = providerFor(AgentProvider.CODEX)!

  it('extracts Codex native rate limit info', () => {
    const result = plugin?.session?.rateLimitsFromMessage!(parsed({
      method: 'account/rateLimits/updated',
      params: {
        rateLimits: {
          primary: { usedPercent: 85, windowDurationMins: 300, resetsAt: 1774070211 },
          secondary: { usedPercent: 4, windowDurationMins: 10080, resetsAt: 1774525963 },
        },
      },
    }))
    expect(result?.mode).toBe('replace')
    expect(result?.values.five_hour?.utilization).toBeCloseTo(0.85)
    expect(result?.values.five_hour?.status).toBe('allowed_warning')
    expect(result?.values.seven_day?.utilization).toBeCloseTo(0.04)
    expect(result?.values.seven_day?.status).toBe('allowed')
    expect(result?.values.account_block).toEqual({})
  })

  it('returns the account-block clearing entry without tiers', () => {
    expect(plugin?.session?.rateLimitsFromMessage!(parsed({
      method: 'account/rateLimits/updated',
      params: { rateLimits: {} },
    }))).toEqual({ mode: 'replace', values: { account_block: {} } })
  })

  it('elevates the most-utilized window to exceeded when reached-type fires under 100%', () => {
    const result = plugin?.session?.rateLimitsFromMessage!(parsed({
      method: 'account/rateLimits/updated',
      params: {
        rateLimits: {
          rateLimitReachedType: 'rate_limit_reached',
          primary: { usedPercent: 99, windowDurationMins: 300, resetsAt: 1774070211 },
          secondary: { usedPercent: 20, windowDurationMins: 10080, resetsAt: 1774525963 },
        },
      },
    }))
    expect(result?.values.five_hour?.status).toBe('exceeded')
    expect(result?.values.seven_day?.status).toBe('allowed')
  })

  it('does not elevate for non-time-window reached-type', () => {
    const result = plugin?.session?.rateLimitsFromMessage!(parsed({
      method: 'account/rateLimits/updated',
      params: {
        rateLimits: {
          rateLimitReachedType: 'workspace_owner_credits_depleted',
          primary: { usedPercent: 20, windowDurationMins: 300, resetsAt: 1774070211 },
        },
      },
    }))
    expect(result?.values.five_hour?.status).toBe('allowed')
    expect(result?.values.account_block).toEqual({
      rateLimitType: 'workspace_owner_credits_depleted',
      status: 'exceeded',
    })
  })

  it('returns null for a non-rate-limit method', () => {
    expect(plugin?.session?.rateLimitsFromMessage!(parsed({ method: 'turn/completed' }))).toBeNull()
  })
})

// Which related half a Codex row wants. An MCP result carries no server or tool name
// of its own; an image view and a collaboration call draw from both rows; a command
// execution carries its whole body in each row and wants nothing.
describe('codex relatedMessages', () => {
  const plugin = providerFor(AgentProvider.CODEX)!
  const item = (fields: Record<string, unknown>) => ({ item: { id: 'call', ...fields } })

  it('an MCP result wants its request', () => {
    expect(plugin?.transcript.relatedMessages!(input(item({ type: 'mcpToolCall', status: 'completed', server: 's', tool: 't' })))).toEqual(['request'])
  })

  it('an image view and a collaboration call want both halves', () => {
    expect(plugin?.transcript.relatedMessages!(input(item({ type: 'imageView', status: 'completed', path: '/a.png' })))).toEqual(['request', 'result'])
    expect(plugin?.transcript.relatedMessages!(input(item({ type: 'collabAgentToolCall', status: 'completed', tool: 'spawnAgent' })))).toEqual(['request', 'result'])
  })

  it('a command execution wants nothing, because each row carries its own body', () => {
    expect(plugin?.transcript.relatedMessages!(input(item({ type: 'commandExecution', status: 'completed', command: 'ls' })))).toEqual([])
  })
})
