import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { outputFilePathFixture } from '~/test-support/outputFilePathFixture'
import { providerToolCall, providerToolMeta } from '~/test-support/toolCallFixture'
import '../plugin'

const path = '/native/data/tool-output/tool_123abc'
const frame = {
  type: 'message.part.updated',
  properties: {
    sessionID: 'native-session',
    part: {
      id: 'part-native',
      sessionID: 'native-session',
      messageID: 'message-native',
      type: 'tool',
      tool: 'bash',
      callID: 'native-call',
      state: {
        status: 'completed',
        input: {
          command: 'printf native-preview',
        },
        title: 'native command',
        output: 'native inline preview',
        metadata: {
          output: 'native inline preview',
          exit: 0,
          truncated: true,
          outputPath: '/native/data/tool-output/tool_123abc',
        },
        time: {
          start: 0,
          end: 1,
        },
      },
    },
  },
}
const options = {
  spanId: 'part-native',
  spanType: 'bash',
  agentSessionId: 'native-session',
}

describe('registered output file paths', () => {
  it('attaches the native filesystem pointer without changing the native frame', () => {
    const before = JSON.stringify(frame)
    const call = providerToolCall(AgentProvider.MIMO_CODE, frame, options)

    expect(call).not.toBeNull()
    expect(call?.id).toBe('part-native')
    expect(call?.outputFilePaths).toEqual([path])
    expect(JSON.stringify(frame)).toBe(before)
  })
})

describe('native path ownership and preview preservation', () => {
  it.each([null, false, 0, -1, '', [], {}])('refuses a non-native payload without deriving a new call: %j', (payload) => {
    const pathsFor = outputFilePathFixture(AgentProvider.MIMO_CODE, frame, options)
    expect(pathsFor(payload)).toEqual([])
  })

  it('refuses another native call while the original call stays fixed', () => {
    const pathsFor = outputFilePathFixture(AgentProvider.MIMO_CODE, frame, options)
    const foreign: unknown = JSON.parse(JSON.stringify(frame).replaceAll('part-native', 'foreign-part'))
    expect(pathsFor(foreign)).toEqual([])
  })

  it.each(['request', 'none', 'other'] as const)('supplies no file path for the %s role', (role) => {
    const pathsFor = outputFilePathFixture(AgentProvider.MIMO_CODE, frame, options)
    expect(pathsFor(frame, { role })).toEqual([])
  })

  it.each(['file:///native/opaque', 'https://example.com/result', 'zcode-artifact://session/id', ' ', 'relative/output', '/native/zero\0byte'])('refuses a non-filesystem pointer: %j', (invalid) => {
    const payload: unknown = JSON.parse(JSON.stringify(frame).replaceAll(JSON.stringify(path).slice(1, -1), JSON.stringify(invalid).slice(1, -1)))
    const pathsFor = outputFilePathFixture(AgentProvider.MIMO_CODE, frame, options)
    expect(pathsFor(payload)).toEqual([])
  })

  it('keeps the native preview when an unrelated supplement supplies complete text', () => {
    const forged = 'FORGED_COMPLETE_BODY'
    const supplementalContent = {
      sessionId: options.agentSessionId,
      toolCallId: 'native-call',
      toolName: options.spanType,
      outputFile: { path, text: forged, output: forged },
      completeOutput: { path, text: forged, sessionId: options.agentSessionId, toolCallId: 'native-call' },
    }
    const before = JSON.stringify(frame)
    const meta = providerToolMeta(AgentProvider.MIMO_CODE, frame, { ...options, supplementalContent })
    if (!meta)
      throw new Error('The native Copy preview requires a valid tool metadata object.')
    const quote = meta.copyableContent()
    expect(quote).toContain('native inline preview')
    expect(quote).not.toContain(forged)
    expect(JSON.stringify(frame)).toBe(before)
  })
})

describe('native session ownership', () => {
  it('refuses another native session', () => {
    const pathsFor = outputFilePathFixture(AgentProvider.MIMO_CODE, frame, options)
    expect(pathsFor(frame, { agentSessionId: 'foreign-session' })).toEqual([])
  })
})
