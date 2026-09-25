import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { createControlAnswerState } from '../../controls/types'
import { acpResultDivider } from '../acp/extractors/resultDivider'
import { describeACPProviderBasics, renderACPRow } from '../acp/testUtils'
import { sendOpenCodeQuestionResponse } from '../openCodeQuestions'
import { providerFor } from '../registry'

import { input } from '../testUtils'
// Side-effect import to register the Kilo plugin.
import './plugin'

describe('kilo classify', () => {
  const plugin = providerFor(AgentProvider.KILO)!

  // Attachment caps, assembled-text handling, config_option_update hiding (empty
  // payload), and the ACP interrupt request are the standard stub behaviours. Kilo's POPULATED
  // config_option_update case below is the genuine divergence and stays inline.
  describeACPProviderBasics(AgentProvider.KILO, { text: true, image: true, pdf: true, binary: true })

  it('classifies tool_call as tool_use with kind', () => {
    const parent = {
      sessionUpdate: 'tool_call',
      toolCallId: 'tc-1',
      title: 'bash',
      kind: 'execute',
      status: 'pending',
      locations: [],
      rawInput: {},
    }
    expect(plugin?.transcript.classify(input(parent))).toEqual({ kind: 'tool_use' })
  })

  it('classifies tool_call without kind using fallback toolName', () => {
    const parent = {
      sessionUpdate: 'tool_call',
      toolCallId: 'tc-1',
      title: 'custom_tool',
      status: 'pending',
    }
    expect(plugin?.transcript.classify(input(parent))).toEqual({ kind: 'tool_use' })
  })

  it('classifies tool_call_update completed as tool_use', () => {
    const parent = {
      sessionUpdate: 'tool_call_update',
      toolCallId: 'tc-1',
      status: 'completed',
      kind: 'execute',
      title: 'bash',
      content: [{ type: 'content', content: { type: 'text', text: 'output' } }],
    }
    expect(plugin?.transcript.classify(input(parent))).toEqual({ kind: 'tool_use' })
  })

  it('classifies tool_call_update failed as tool_use', () => {
    const parent = {
      sessionUpdate: 'tool_call_update',
      toolCallId: 'tc-1',
      status: 'failed',
      kind: 'execute',
    }
    expect(plugin?.transcript.classify(input(parent))).toEqual({ kind: 'tool_use' })
  })

  it('hides tool_call_update in_progress', () => {
    const parent = {
      sessionUpdate: 'tool_call_update',
      toolCallId: 'tc-1',
      status: 'in_progress',
      kind: 'execute',
    }
    expect(plugin?.transcript.classify(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('classifies plan as tool_use', () => {
    const parent = {
      sessionUpdate: 'plan',
      entries: [
        { priority: 'medium', status: 'pending', content: 'Step 1' },
      ],
    }
    expect(plugin?.transcript.classify(input(parent))).toEqual({ kind: 'tool_use' })
  })

  it('hides usage_update', () => {
    const parent = {
      sessionUpdate: 'usage_update',
      used: 1000,
      size: 128000,
    }
    expect(plugin?.transcript.classify(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('hides available_commands_update', () => {
    const parent = {
      sessionUpdate: 'available_commands_update',
      availableCommands: [],
    }
    expect(plugin?.transcript.classify(input(parent))).toEqual({ kind: 'hidden' })
  })

  // The backend consumes config_option_update centrally for every ACP provider, so it
  // is hidden in the shared base classifier -- including for OpenCode/Kilo, which never
  // opted into the per-provider hidden set. Historical rows must not render as unknown.
  it('hides config_option_update', () => {
    const parent = {
      sessionUpdate: 'config_option_update',
      configOptions: [{ id: 'model', currentValue: 'm1', options: [{ value: 'm1' }] }],
    }
    expect(plugin?.transcript.classify(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('hides user_message_chunk', () => {
    const parent = {
      sessionUpdate: 'user_message_chunk',
      content: { type: 'text', text: 'hello' },
    }
    expect(plugin?.transcript.classify(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('classifies result divider (stopReason)', () => {
    const parent = {
      stopReason: 'end_turn',
      usage: { totalTokens: 100 },
    }
    expect(plugin?.transcript.classify(input(parent))).toEqual({ kind: 'result_divider' })
  })

  it('classifies settings_changed as notification', () => {
    const parent = { type: 'settings_changed', changes: { model: { old: 'a', new: 'b' } } }
    expect(plugin?.transcript.classify(input(parent)).kind).toBe('notification')
  })

  it('classifies agent_error as notification', () => {
    const parent = { type: 'agent_error', error: 'something went wrong' }
    expect(plugin?.transcript.classify(input(parent))).toEqual({
      kind: 'notification',
      entries: [{ kind: 'text', text: 'something went wrong' }],
    })
  })

  it('classifies user content', () => {
    const parent = { content: 'Hello agent' }
    expect(plugin?.transcript.classify(input(parent))).toEqual({ kind: 'user_content' })
  })

  it('hides hidden user content', () => {
    const parent = { content: 'internal', hidden: true }
    expect(plugin?.transcript.classify(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('hides JSON-RPC response envelope', () => {
    const parent = { id: 5, result: { outcome: { optionId: 'once' } } }
    expect(plugin?.transcript.classify(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('returns unknown for unrecognized parent', () => {
    const parent = { something: 'weird' }
    expect(plugin?.transcript.classify(input(parent))).toEqual({ kind: 'unknown' })
  })

  it('handles notification thread wrappers', () => {
    const wrapper = {
      old_seqs: [1],
      messages: [{ type: 'interrupted' }],
    }
    expect(plugin?.transcript.classify(input(undefined, wrapper))).toEqual({
      kind: 'notification',
      entries: [{ kind: 'text', text: 'Interrupted' }],
    })
  })

  it('hides empty wrapper', () => {
    const wrapper = { old_seqs: [], messages: [] }
    expect(plugin?.transcript.classify(input(undefined, wrapper))).toEqual({ kind: 'hidden' })
  })
})

/**
 * KILO SENDS NO `system` FRAME, and neither does any other daemon of this family. The
 * two objects below are hand-built, in Claude Code's stream-json shape, so a reader
 * must not take them as evidence that the shape occurs. They pin the answer of a
 * forward-compatibility guard: the worker persists one unrecognized stdout line byte
 * for byte and never reads a top-level `type`, so the browser must still classify one.
 * `isHiddenACPNotification` in `../acp/classification` holds the whole standing
 * property.
 */
describe('the system-frame guard (kilo)', () => {
  const plugin = providerFor(AgentProvider.KILO)!

  it('hides the init lifecycle frame', () => {
    const parent = { type: 'system', subtype: 'init' }
    expect(plugin?.transcript.classify(input(parent))).toStrictEqual({ kind: 'hidden' })
  })

  it('keeps a system frame with no entry for the last-resort card', () => {
    const parent = { type: 'system', subtype: 'compact_boundary' }
    expect(plugin?.transcript.classify(input(parent))).toStrictEqual({ kind: 'notification', entries: [] })
  })
})

describe('kilo result divider', () => {
  const plugin = providerFor(AgentProvider.KILO)!

  it('maps end_turn to "Turn ended"', () => {
    expect(acpResultDivider({ stopReason: 'end_turn', usage: { totalTokens: 100 } })).toEqual({ label: 'Turn ended' })
  })

  it('maps a missing stopReason to "Turn ended"', () => {
    expect(acpResultDivider({ usage: { totalTokens: 100 } })).toEqual({ label: 'Turn ended' })
  })

  it('maps a non-end_turn stopReason to "Turn ended (reason)"', () => {
    expect(acpResultDivider({ stopReason: 'max_tokens' })).toEqual({ label: 'Turn ended (max_tokens)' })
  })

  it('returns null for non-object input', () => {
    expect(acpResultDivider(null)).toBeNull()
    expect(acpResultDivider('string')).toBeNull()
  })

  it('is registered as the plugin resultDivider hook', () => {
    expect(plugin?.transcript.extractDivider!({ stopReason: 'end_turn' })).toEqual({ label: 'Turn ended' })
  })
})

describe('kilo tool_call renderer', () => {
  const plugin = providerFor(AgentProvider.KILO)!

  it('renders tool_call with execute kind', () => {
    const toolUse = {
      sessionUpdate: 'tool_call',
      toolCallId: 'call_1',
      title: 'bash',
      kind: 'execute',
      status: 'pending',
      locations: [],
      rawInput: {},
    }
    const category = plugin?.transcript.classify(input(toolUse))
    expect(category.kind).toBe('tool_use')
    const { container } = renderACPRow(AgentProvider.KILO, toolUse)
    // Title and kind label appear in the header.
    expect(container.textContent).toContain('bash')
  })

  it('renders tool_call without kind', () => {
    const toolUse = {
      sessionUpdate: 'tool_call',
      toolCallId: 'call_2',
      title: 'custom_tool',
      status: 'pending',
    }
    const { container } = renderACPRow(AgentProvider.KILO, toolUse)
    expect(container.textContent).toContain('custom_tool')
  })
})

describe('kilo plan mode', () => {
  const plugin = providerFor(AgentProvider.KILO)!

  it('reads the current mode from optionValues.primaryAgent', () => {
    expect(plugin?.configuration?.planMode?.currentMode({ optionValues: { primaryAgent: 'plan' } })).toBe('plan')
    expect(plugin?.configuration?.planMode?.currentMode({ optionValues: {} })).toBe('code')
  })

  it('declares primaryAgent as the plan-mode group with plan/code values', () => {
    // The generic settings panel renders the primaryAgent option group and
    // dispatches changes through the host; the provider only declares which
    // group + values drive plan mode.
    expect(plugin?.configuration?.planMode).toMatchObject({
      groupKey: 'primaryAgent',
      planValue: 'plan',
      defaultValue: 'code',
    })
  })

  it('renders the primaryAgent group as the trigger mode segment', () => {
    expect(plugin?.configuration?.triggerModeGroupKey).toBe('primaryAgent')
  })
})

describe('kilo tool_call_update renderer', () => {
  const plugin = providerFor(AgentProvider.KILO)!

  it('renders completed execute tool_call_update with command and output', () => {
    const toolUse = {
      sessionUpdate: 'tool_call_update',
      toolCallId: 'call_1',
      status: 'completed',
      kind: 'execute',
      title: 'Shows recent commit messages',
      rawInput: {
        command: 'git log --oneline -5',
        workdir: '/workspace',
        description: 'Shows recent commit messages',
      },
      rawOutput: {
        output: 'abc123 fix something\ndef456 add feature',
        metadata: { exit: 0, description: 'Shows recent commit messages', truncated: false },
      },
      content: [{ type: 'content', content: { type: 'text', text: 'abc123 fix something\ndef456 add feature' } }],
    }
    const category = plugin?.transcript.classify(input(toolUse))
    expect(category.kind).toBe('tool_use')
    const { container } = renderACPRow(AgentProvider.KILO, toolUse)
    // Title is rendered in the header; command appears in the body.
    expect(container.textContent).toContain('Shows recent commit messages')
    expect(container.textContent).toContain('git log --oneline -5')
  })

  it('renders failed execute tool_call_update', () => {
    const toolUse = {
      sessionUpdate: 'tool_call_update',
      toolCallId: 'call_2',
      status: 'failed',
      kind: 'execute',
      title: 'Run failing command',
      rawInput: { command: 'false' },
      rawOutput: { error: 'command failed', metadata: { exit: 1 } },
      content: [],
    }
    const { container } = renderACPRow(AgentProvider.KILO, toolUse)
    expect(container.textContent).toContain('Run failing command')
    expect(container.textContent).toContain('false')
  })

  it('classifies edit kind tool_call_update as tool_use', () => {
    const toolUse = {
      sessionUpdate: 'tool_call_update',
      toolCallId: 'call_3',
      status: 'completed',
      kind: 'edit',
      title: 'src/main.ts',
      content: [
        { type: 'diff', path: 'src/main.ts', oldText: 'const a = 1', newText: 'const a = 2' },
      ],
    }
    const category = plugin?.transcript.classify(input(toolUse))
    expect(category.kind).toBe('tool_use')
  })
})

describe('kilo isAskUserQuestion', () => {
  const plugin = providerFor(AgentProvider.KILO)!

  it('returns true for question requests', () => {
    const payload = {
      type: 'question.asked',
      properties: { questions: [] },
    }
    expect(plugin?.controls?.askUserQuestion!.isRequest(payload)).toBe(true)
  })

  it('returns false for permission requests', () => {
    const payload = {
      method: 'requestPermission',
      params: { toolCall: { toolCallId: 'tc-1' } },
    }
    expect(plugin?.controls?.askUserQuestion!.isRequest(payload)).toBe(false)
  })

  it('returns false for regular messages', () => {
    expect(plugin?.controls?.askUserQuestion!.isRequest({})).toBe(false)
  })
})

describe('the kilo permission sender', () => {
  function decode(bytes: Uint8Array): Record<string, unknown> {
    return JSON.parse(new TextDecoder().decode(bytes))
  }

  it('sends allow_once outcome with the unchanged worker request ID', async () => {
    let captured: Uint8Array | undefined
    const onRespond = vi.fn(async (content: Uint8Array) => {
      captured = content
    })

    await providerFor(AgentProvider.KILO)!.controls?.sendPermissionOption!(onRespond, '5', 'once')

    expect(onRespond).toHaveBeenCalledOnce()
    const parsed = decode(captured!)
    expect(parsed).toMatchObject({
      jsonrpc: '2.0',
      id: '5',
      result: { outcome: { outcome: 'selected', optionId: 'once' } },
    })
  })
})

describe('kilo sendOpenCodeQuestionResponse', () => {
  function decode(bytes: Uint8Array): Record<string, unknown> {
    return JSON.parse(new TextDecoder().decode(bytes))
  }

  it('sends ordered answer arrays for each question', async () => {
    let captured: Uint8Array | undefined
    const onRespond = vi.fn(async (content: Uint8Array) => {
      captured = content
    })

    await sendOpenCodeQuestionResponse(onRespond, 'que_1', [
      { question: 'Action?', options: [{ label: 'Build' }] },
      { question: 'Env?', options: [{ label: 'Dev' }] },
    ], createControlAnswerState({ selections: { 0: ['Build'], 1: [] }, customTexts: { 1: 'Dev' } }))

    const parsed = decode(captured!)
    expect(parsed).toMatchObject({
      jsonrpc: '2.0',
      id: 'que_1',
      result: {
        answers: [['Build'], ['Dev']],
      },
    })
  })
})
