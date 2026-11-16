import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { acpTextContent, describeACPCallRows, describeACPProviderBasics } from '../acp/testUtils'
import { providerFor } from '../registry'

import './plugin'

vi.mock('~/api/workerRpc', () => ({
  workerAgent: {
    updateAgentSettings: vi.fn(),
  },
}))

describe('reasonix provider', () => {
  const plugin = providerFor(AgentProvider.REASONIX)!

  describeACPProviderBasics(AgentProvider.REASONIX, {
    text: true,
    image: true,
    pdf: true,
    audio: false,
    binary: true,
  })

  it('uses the advertised mode and tool approval controls', () => {
    expect(plugin?.configuration?.planMode).toBeDefined()
    expect(plugin?.configuration?.triggerModeGroupKey).toBe('permissionMode')
    expect(plugin?.controls?.permissionPresets?.bypass?.sets).toEqual({ tool_approval: 'yolo' })
  })

  it('offers native image and resource attachments', () => {
    expect(plugin.configuration?.attachments).toEqual({
      text: true,
      image: true,
      pdf: true,
      audio: false,
      binary: true,
    })
  })

  it('reads the native MCP form request', () => {
    const schema = {
      type: 'object',
      properties: {
        count: { type: 'string', maxLength: 4096 },
      },
    }
    expect(plugin?.controls?.elicitation?.({
      method: 'elicitation/create',
      params: {
        sessionId: 'session-1',
        mode: 'form',
        message: 'Choose a count.',
        requestedSchema: schema,
      },
    })).toEqual({
      mode: 'form',
      message: 'Choose a count.',
      server: '',
      schema,
      url: '',
      title: '',
      description: '',
    })
  })
})

describe('native output file paths', () => {
  it('exposes the native output file that replaces a large result', () => {
    const path = '/home/user/.reasonix/sessions/session.outputs/native-call.txt'
    const text = `[bash output kept out of context: 6001 lines, 632 KB]\nFull output: ${path}\nRead a window of it with read_file (offset/limit), or search it with grep. It is not in this conversation.\n\nOpening lines:\nfirst line\n`
    const frame = {
      sessionUpdate: 'tool_call_update',
      toolCallId: 'native-call',
      status: 'completed',
      kind: 'execute',
      title: 'bash',
      rawInput: { command: 'generate-output' },
      content: [
        {
          type: 'content',
          content: { type: 'text', text },
        },
      ],
    }
    const call = providerToolCall(AgentProvider.REASONIX, frame, {
      spanId: 'native-call',
      spanType: 'bash',
      agentSessionId: 'native-session',
      role: 'result',
    })
    expect(call?.outputFilePaths).toEqual([path])
  })

  it('keeps an inline result without inventing an output path', () => {
    const frame = {
      sessionUpdate: 'tool_call_update',
      toolCallId: 'native-call',
      status: 'completed',
      kind: 'execute',
      title: 'native command',
      rawInput: {
        command: 'printf preview',
      },
      content: [
        {
          type: 'content',
          content: {
            type: 'text',
            text: 'native inline preview',
          },
        },
      ],
      rawOutput: {
        output: 'native inline preview',
        exitCode: 0,
      },
    }
    const call = providerToolCall(AgentProvider.REASONIX, frame, {
      spanId: 'native-call',
      spanType: 'bash',
      agentSessionId: 'native-session',
      role: 'result',
    })
    expect(call).not.toBeNull()
    expect(call?.id).toBe('native-call')
    expect(call?.outputFilePaths).toBeUndefined()
    const registered = providerFor(AgentProvider.REASONIX)
    expect(registered).toBeDefined()
    expect(registered?.transcript.outputFilePaths).toBeDefined()
  })
})

// The record that the worker saves beside a finished read states the path that
// Reasonix resolved. The request row draws the path that the call asked for, and the
// result row the resolved one.
describe('reasonix call rows', () => {
  describeACPCallRows(AgentProvider.REASONIX, [
    {
      name: 'a read whose saved record states the resolved path',
      request: {
        sessionUpdate: 'tool_call',
        toolCallId: 'reasonix-read',
        kind: 'read',
        title: 'read_file',
        status: 'pending',
        rawInput: { path: 'a.ts' },
      },
      result: {
        sessionUpdate: 'tool_call_update',
        toolCallId: 'reasonix-read',
        status: 'completed',
        content: acpTextContent('1\tconst a = 1\n'),
      },
      resultSupplement: {
        rawOutput: {
          reasonix: {
            role: 'tool',
            tool_call_id: 'reasonix-read',
            name: 'read_file',
            content: '1\tconst a = 1\n',
            read_result: {
              source: { canonical_path: '/p/a.ts' },
            },
          },
        },
      },
      requestCall: {
        kind: 'read',
        status: 'pending',
        request: { path: 'a.ts' },
      },
      resultCall: {
        kind: 'read',
        status: 'completed',
        request: { path: '/p/a.ts' },
      },
    },
  ])
})
