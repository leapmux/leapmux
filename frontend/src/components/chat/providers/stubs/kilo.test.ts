import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider, MessageRole } from '~/generated/leapmux/v1/agent_pb'
import { sendOpenCodePermissionResponse, sendOpenCodeQuestionResponse } from '../../controls/OpenCodeControlRequest'
import { getProviderPlugin } from '../registry'
import { acpResultDividerRenderer } from '../shared/acpRenderers'
import { input, model, option, optionGroup } from '../testUtils'

// Side-effect import to register the Kilo plugin.
import './kilo'

describe('kilo classify', () => {
  const plugin = getProviderPlugin(AgentProvider.KILO)!

  it('exposes attachment capabilities', () => {
    expect(plugin.attachments).toEqual({
      text: true,
      image: true,
      pdf: true,
      binary: true,
    })
  })

  it('classifies agent_message_chunk as assistant_text', () => {
    const parent = {
      sessionUpdate: 'agent_message_chunk',
      content: { type: 'text', text: 'Hello' },
    }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'assistant_text' })
  })

  it('classifies agent_thought_chunk as assistant_thinking', () => {
    const parent = {
      sessionUpdate: 'agent_thought_chunk',
      content: { type: 'text', text: 'thinking...' },
    }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'assistant_thinking' })
  })

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
    expect(plugin.classify(input(parent))).toEqual({
      kind: 'tool_use',
      toolName: 'execute',
      toolUse: parent,
      content: [],
    })
  })

  it('classifies tool_call without kind using fallback toolName', () => {
    const parent = {
      sessionUpdate: 'tool_call',
      toolCallId: 'tc-1',
      title: 'custom_tool',
      status: 'pending',
    }
    expect(plugin.classify(input(parent))).toEqual({
      kind: 'tool_use',
      toolName: 'tool_call',
      toolUse: parent,
      content: [],
    })
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
    expect(plugin.classify(input(parent))).toEqual({
      kind: 'tool_use',
      toolName: 'execute',
      toolUse: parent,
      content: [],
    })
  })

  it('classifies tool_call_update failed as tool_use', () => {
    const parent = {
      sessionUpdate: 'tool_call_update',
      toolCallId: 'tc-1',
      status: 'failed',
      kind: 'execute',
    }
    expect(plugin.classify(input(parent))).toEqual({
      kind: 'tool_use',
      toolName: 'execute',
      toolUse: parent,
      content: [],
    })
  })

  it('hides tool_call_update in_progress', () => {
    const parent = {
      sessionUpdate: 'tool_call_update',
      toolCallId: 'tc-1',
      status: 'in_progress',
      kind: 'execute',
    }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('classifies plan as tool_use', () => {
    const parent = {
      sessionUpdate: 'plan',
      entries: [
        { priority: 'medium', status: 'pending', content: 'Step 1' },
      ],
    }
    expect(plugin.classify(input(parent))).toEqual({
      kind: 'tool_use',
      toolName: 'plan',
      toolUse: parent,
      content: [],
    })
  })

  it('hides usage_update', () => {
    const parent = {
      sessionUpdate: 'usage_update',
      used: 1000,
      size: 128000,
    }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('hides available_commands_update', () => {
    const parent = {
      sessionUpdate: 'available_commands_update',
      availableCommands: [],
    }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('hides user_message_chunk', () => {
    const parent = {
      sessionUpdate: 'user_message_chunk',
      content: { type: 'text', text: 'hello' },
    }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('classifies result divider (stopReason)', () => {
    const parent = {
      stopReason: 'end_turn',
      usage: { totalTokens: 100 },
    }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'result_divider' })
  })

  it('hides system init', () => {
    const parent = { type: 'system', subtype: 'init' }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('classifies system notification', () => {
    const parent = { type: 'system', subtype: 'compact_boundary' }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'notification' })
  })

  it('classifies settings_changed as notification', () => {
    const parent = { type: 'settings_changed' }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'notification' })
  })

  it('classifies agent_error as notification', () => {
    const parent = { type: 'agent_error', error: 'something went wrong' }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'notification' })
  })

  it('classifies user content', () => {
    const parent = { content: 'Hello agent' }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'user_content' })
  })

  it('hides hidden user content', () => {
    const parent = { content: 'internal', hidden: true }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('hides JSON-RPC response envelope', () => {
    const parent = { id: 5, result: { outcome: { optionId: 'once' } } }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('returns unknown for unrecognized parent', () => {
    const parent = { something: 'weird' }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'unknown' })
  })

  it('handles notification thread wrappers', () => {
    const wrapper = {
      old_seqs: [1],
      messages: [{ type: 'interrupted' }],
    }
    expect(plugin.classify(input(undefined, wrapper))).toEqual({
      kind: 'notification_thread',
      messages: wrapper.messages,
    })
  })

  it('hides empty wrapper', () => {
    const wrapper = { old_seqs: [], messages: [] }
    expect(plugin.classify(input(undefined, wrapper))).toEqual({ kind: 'hidden' })
  })
})

describe('kilo result divider renderer', () => {
  const plugin = getProviderPlugin(AgentProvider.KILO)!

  it('renders "Turn ended" for end_turn', () => {
    const parsed = { stopReason: 'end_turn', usage: { totalTokens: 100 } }
    const result = acpResultDividerRenderer(parsed)
    expect(result).not.toBeNull()
  })

  it('renders "Turn ended" when stopReason is missing', () => {
    const parsed = { usage: { totalTokens: 100 } }
    const result = acpResultDividerRenderer(parsed)
    expect(result).not.toBeNull()
  })

  it('returns null for non-object input', () => {
    expect(acpResultDividerRenderer(null)).toBeNull()
    expect(acpResultDividerRenderer('string')).toBeNull()
  })

  it('is returned by plugin.renderMessage for result_divider', () => {
    const parsed = { stopReason: 'end_turn' }
    const result = plugin.renderMessage!({ kind: 'result_divider' }, parsed, MessageRole.RESULT)
    expect(result).not.toBeNull()
  })
})

describe('kilo tool_call renderer', () => {
  const plugin = getProviderPlugin(AgentProvider.KILO)!

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
    const category = plugin.classify(input(toolUse))
    expect(category.kind).toBe('tool_use')
    const result = plugin.renderMessage!(category, toolUse, MessageRole.ASSISTANT)
    expect(result).not.toBeNull()
  })

  it('renders tool_call without kind', () => {
    const toolUse = {
      sessionUpdate: 'tool_call',
      toolCallId: 'call_2',
      title: 'custom_tool',
      status: 'pending',
    }
    const category = plugin.classify(input(toolUse))
    const result = plugin.renderMessage!(category, toolUse, MessageRole.ASSISTANT)
    expect(result).not.toBeNull()
  })
})

describe('kilo plan mode', () => {
  const plugin = getProviderPlugin(AgentProvider.KILO)!

  it('reads the current mode from extraSettings.primaryAgent', () => {
    expect(plugin.planMode?.currentMode({ extraSettings: { primaryAgent: 'plan' } })).toBe('plan')
    expect(plugin.planMode?.currentMode({ extraSettings: {} })).toBe('code')
  })

  it('setMode writes primaryAgent through onOptionGroupChange', () => {
    const onOptionGroupChange = vi.fn()
    plugin.planMode?.setMode('plan', { onOptionGroupChange })
    expect(onOptionGroupChange).toHaveBeenCalledWith('primaryAgent', 'plan')
  })
})

describe('kilo settings panel', () => {
  const plugin = getProviderPlugin(AgentProvider.KILO)!

  it('renders primary-agent choices and updates via option-group callback', async () => {
    const onOptionGroupChange = vi.fn()
    render(() => plugin.SettingsPanel!({
      model: 'openai/gpt-5',
      extraSettings: { primaryAgent: 'code' },
      availableModels: [model('openai/gpt-5', 'GPT-5', { isDefault: true })],
      availableOptionGroups: [optionGroup('primaryAgent', 'Primary Agent', [
        option('code', 'code', { isDefault: true }),
        option('plan', 'plan'),
      ])],
      onOptionGroupChange,
    }))

    expect(screen.getByText('Primary Agent')).toBeInTheDocument()
    expect(screen.getByTestId('primary-agent-code')).toBeInTheDocument()
    expect(screen.getByTestId('primary-agent-plan')).toBeInTheDocument()

    await fireEvent.click(screen.getByDisplayValue('plan'))
    expect(onOptionGroupChange).toHaveBeenCalledWith('primaryAgent', 'plan')
  })

  it('includes the selected primary agent in the trigger label', () => {
    render(() => plugin.settingsTriggerLabel!({
      model: 'openai/gpt-5',
      extraSettings: { primaryAgent: 'plan' },
      availableModels: [model('openai/gpt-5', 'GPT-5', { isDefault: true })],
      availableOptionGroups: [optionGroup('primaryAgent', 'Primary Agent', [
        option('code', 'Code', { isDefault: true }),
        option('plan', 'Plan'),
      ])],
    }))

    expect(screen.getByText('GPT-5 \u00B7 Plan')).toBeInTheDocument()
  })
})

describe('kilo tool_call_update renderer', () => {
  const plugin = getProviderPlugin(AgentProvider.KILO)!

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
    const category = plugin.classify(input(toolUse))
    expect(category.kind).toBe('tool_use')
    const result = plugin.renderMessage!(category, toolUse, MessageRole.ASSISTANT)
    expect(result).not.toBeNull()
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
    const category = plugin.classify(input(toolUse))
    const result = plugin.renderMessage!(category, toolUse, MessageRole.ASSISTANT)
    expect(result).not.toBeNull()
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
    const category = plugin.classify(input(toolUse))
    expect(category.kind).toBe('tool_use')
  })
})

describe('kilo isAskUserQuestion', () => {
  const plugin = getProviderPlugin(AgentProvider.KILO)!

  it('returns true for question requests', () => {
    const payload = {
      type: 'question.asked',
      properties: { questions: [] },
    }
    expect(plugin.isAskUserQuestion!(payload)).toBe(true)
  })

  it('returns false for permission requests', () => {
    const payload = {
      method: 'requestPermission',
      params: { toolCall: { toolCallId: 'tc-1' } },
    }
    expect(plugin.isAskUserQuestion!(payload)).toBe(false)
  })

  it('returns false for regular messages', () => {
    expect(plugin.isAskUserQuestion!({})).toBe(false)
  })
})

describe('kilo buildInterruptContent', () => {
  const plugin = getProviderPlugin(AgentProvider.KILO)!

  it('builds a cancel notification', () => {
    const content = plugin.buildInterruptContent!('session-123')
    const parsed = JSON.parse(content!)
    expect(parsed).toMatchObject({
      jsonrpc: '2.0',
      method: 'session/cancel',
      params: { sessionId: 'session-123' },
    })
  })

  it('returns null for empty session id', () => {
    expect(plugin.buildInterruptContent!('')).toBeNull()
  })
})

describe('kilo sendOpenCodePermissionResponse', () => {
  function decode(bytes: Uint8Array): Record<string, unknown> {
    return JSON.parse(new TextDecoder().decode(bytes))
  }

  it('sends allow_once outcome with numeric id', async () => {
    let captured: Uint8Array | undefined
    const onRespond = vi.fn(async (_id: string, content: Uint8Array) => {
      captured = content
    })

    await sendOpenCodePermissionResponse('agent1', onRespond, '5', 'once')

    expect(onRespond).toHaveBeenCalledOnce()
    const parsed = decode(captured!)
    expect(parsed).toMatchObject({
      jsonrpc: '2.0',
      id: 5,
      result: { outcome: { outcome: 'selected', optionId: 'once' } },
    })
  })
})

describe('kilo sendOpenCodeQuestionResponse', () => {
  function decode(bytes: Uint8Array): Record<string, unknown> {
    return JSON.parse(new TextDecoder().decode(bytes))
  }

  function makeAskState() {
    return {
      selections: () => ({ 0: ['Build'], 1: [] }),
      setSelections: vi.fn(),
      customTexts: () => ({ 1: 'Dev' }),
      setCustomTexts: vi.fn(),
      currentPage: () => 0,
      setCurrentPage: vi.fn(),
    }
  }

  it('sends ordered answer arrays for each question', async () => {
    let captured: Uint8Array | undefined
    const onRespond = vi.fn(async (_id: string, content: Uint8Array) => {
      captured = content
    })

    await sendOpenCodeQuestionResponse('agent1', onRespond, 'que_1', [
      { question: 'Action?', options: [{ label: 'Build' }] },
      { question: 'Env?', options: [{ label: 'Dev' }] },
    ], makeAskState())

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
