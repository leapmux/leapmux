import { describe, expect, it, vi } from 'vitest'
import { AgentProvider, MessageRole } from '~/generated/leapmux/v1/agent_pb'
import { sendOpenCodePermissionResponse } from '../controls/OpenCodeControlRequest'
import { opencodeResultDividerRenderer } from '../opencodeRenderers'
import { getProviderPlugin } from './registry'

// Side-effect import to register the OpenCode plugin.
import './opencode'

describe('opencode classify', () => {
  const plugin = getProviderPlugin(AgentProvider.OPENCODE)!

  it('classifies agent_message_chunk as assistant_text', () => {
    const parent = {
      sessionUpdate: 'agent_message_chunk',
      content: { type: 'text', text: 'Hello' },
    }
    expect(plugin.classify(parent, null)).toEqual({ kind: 'assistant_text' })
  })

  it('classifies agent_thought_chunk as assistant_thinking', () => {
    const parent = {
      sessionUpdate: 'agent_thought_chunk',
      content: { type: 'text', text: 'thinking...' },
    }
    expect(plugin.classify(parent, null)).toEqual({ kind: 'assistant_thinking' })
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
    expect(plugin.classify(parent, null)).toEqual({
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
    expect(plugin.classify(parent, null)).toEqual({
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
    expect(plugin.classify(parent, null)).toEqual({
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
    expect(plugin.classify(parent, null)).toEqual({
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
    expect(plugin.classify(parent, null)).toEqual({ kind: 'hidden' })
  })

  it('classifies plan as tool_use', () => {
    const parent = {
      sessionUpdate: 'plan',
      entries: [
        { priority: 'medium', status: 'pending', content: 'Step 1' },
      ],
    }
    expect(plugin.classify(parent, null)).toEqual({
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
    expect(plugin.classify(parent, null)).toEqual({ kind: 'hidden' })
  })

  it('hides available_commands_update', () => {
    const parent = {
      sessionUpdate: 'available_commands_update',
      availableCommands: [],
    }
    expect(plugin.classify(parent, null)).toEqual({ kind: 'hidden' })
  })

  it('hides user_message_chunk', () => {
    const parent = {
      sessionUpdate: 'user_message_chunk',
      content: { type: 'text', text: 'hello' },
    }
    expect(plugin.classify(parent, null)).toEqual({ kind: 'hidden' })
  })

  it('classifies result divider (stopReason)', () => {
    const parent = {
      stopReason: 'end_turn',
      usage: { totalTokens: 100 },
    }
    expect(plugin.classify(parent, null)).toEqual({ kind: 'result_divider' })
  })

  it('hides system init', () => {
    const parent = { type: 'system', subtype: 'init' }
    expect(plugin.classify(parent, null)).toEqual({ kind: 'hidden' })
  })

  it('classifies system notification', () => {
    const parent = { type: 'system', subtype: 'compact_boundary' }
    expect(plugin.classify(parent, null)).toEqual({ kind: 'notification' })
  })

  it('classifies settings_changed as notification', () => {
    const parent = { type: 'settings_changed' }
    expect(plugin.classify(parent, null)).toEqual({ kind: 'notification' })
  })

  it('classifies agent_error as notification', () => {
    const parent = { type: 'agent_error', error: 'something went wrong' }
    expect(plugin.classify(parent, null)).toEqual({ kind: 'notification' })
  })

  it('classifies user content', () => {
    const parent = { content: 'Hello agent' }
    expect(plugin.classify(parent, null)).toEqual({ kind: 'user_content' })
  })

  it('hides hidden user content', () => {
    const parent = { content: 'internal', hidden: true }
    expect(plugin.classify(parent, null)).toEqual({ kind: 'hidden' })
  })

  it('hides JSON-RPC response envelope', () => {
    const parent = { id: 5, result: { outcome: { optionId: 'once' } } }
    expect(plugin.classify(parent, null)).toEqual({ kind: 'hidden' })
  })

  it('returns unknown for unrecognized parent', () => {
    const parent = { something: 'weird' }
    expect(plugin.classify(parent, null)).toEqual({ kind: 'unknown' })
  })

  it('handles notification thread wrappers', () => {
    const wrapper = {
      old_seqs: [1],
      messages: [{ type: 'interrupted' }],
    }
    expect(plugin.classify(undefined, wrapper)).toEqual({
      kind: 'notification_thread',
      messages: wrapper.messages,
    })
  })

  it('hides empty wrapper', () => {
    const wrapper = { old_seqs: [], messages: [] }
    expect(plugin.classify(undefined, wrapper)).toEqual({ kind: 'hidden' })
  })
})

describe('opencode result divider renderer', () => {
  const plugin = getProviderPlugin(AgentProvider.OPENCODE)!

  it('renders "Turn ended" for end_turn', () => {
    const parsed = { stopReason: 'end_turn', usage: { totalTokens: 100 } }
    const result = opencodeResultDividerRenderer(parsed)
    expect(result).not.toBeNull()
  })

  it('renders "Turn ended" when stopReason is missing', () => {
    const parsed = { usage: { totalTokens: 100 } }
    const result = opencodeResultDividerRenderer(parsed)
    expect(result).not.toBeNull()
  })

  it('returns null for non-object input', () => {
    expect(opencodeResultDividerRenderer(null)).toBeNull()
    expect(opencodeResultDividerRenderer('string')).toBeNull()
  })

  it('is returned by plugin.renderMessage for result_divider', () => {
    const parsed = { stopReason: 'end_turn' }
    const result = plugin.renderMessage!({ kind: 'result_divider' }, parsed, MessageRole.RESULT)
    expect(result).not.toBeNull()
  })
})

describe('opencode isAskUserQuestion', () => {
  const plugin = getProviderPlugin(AgentProvider.OPENCODE)!

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

describe('opencode buildInterruptContent', () => {
  const plugin = getProviderPlugin(AgentProvider.OPENCODE)!

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

describe('sendOpenCodePermissionResponse', () => {
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
      result: { outcome: { optionId: 'once' } },
    })
  })

  it('sends reject outcome', async () => {
    let captured: Uint8Array | undefined
    const onRespond = vi.fn(async (_id: string, content: Uint8Array) => {
      captured = content
    })

    await sendOpenCodePermissionResponse('agent1', onRespond, '7', 'reject')

    const parsed = decode(captured!)
    expect(parsed).toMatchObject({
      jsonrpc: '2.0',
      id: 7,
      result: { outcome: { optionId: 'reject' } },
    })
  })

  it('sends always allow outcome', async () => {
    let captured: Uint8Array | undefined
    const onRespond = vi.fn(async (_id: string, content: Uint8Array) => {
      captured = content
    })

    await sendOpenCodePermissionResponse('agent1', onRespond, '9', 'always')

    const parsed = decode(captured!)
    expect(parsed).toMatchObject({
      jsonrpc: '2.0',
      id: 9,
      result: { outcome: { optionId: 'always' } },
    })
  })

  it('preserves non-numeric request id', async () => {
    let captured: Uint8Array | undefined
    const onRespond = vi.fn(async (_id: string, content: Uint8Array) => {
      captured = content
    })

    await sendOpenCodePermissionResponse('agent1', onRespond, 'abc', 'once')

    const parsed = decode(captured!)
    expect(parsed).toMatchObject({
      jsonrpc: '2.0',
      id: 'abc',
      result: { outcome: { optionId: 'once' } },
    })
  })
})
