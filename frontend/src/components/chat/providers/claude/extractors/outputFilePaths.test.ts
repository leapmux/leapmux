import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { outputFilePathFixture } from '~/test-support/outputFilePathFixture'
import { providerToolCall, providerToolMeta } from '~/test-support/toolCallFixture'
import '../plugin'

const path = '/native/claude/native-session/tool-results/native-call.txt'
const frame = {
  type: 'user',
  session_id: 'native-session',
  message: {
    content: [
      {
        type: 'tool_result',
        tool_use_id: 'native-call',
        content: '<persisted-output>\nOutput too large (20KB). Full output saved to: /native/claude/native-session/tool-results/native-call.txt\n\nPreview (first 2KB):\nnative inline preview\n</persisted-output>',
        is_error: false,
      },
    ],
  },
  tool_use_result: {
    stdout: 'native inline preview',
    stderr: '',
    interrupted: false,
    isImage: false,
    persistedOutputPath: '/native/claude/native-session/tool-results/native-call.txt',
    persistedOutputSize: 20000,
  },
}
const options = {
  spanId: 'native-call',
  spanType: 'Bash',
  agentSessionId: 'native-session',
}

describe('registered output file paths', () => {
  it('attaches the native filesystem pointer without changing the native frame', () => {
    const before = JSON.stringify(frame)
    const call = providerToolCall(AgentProvider.CLAUDE_CODE, frame, options)

    expect(call).not.toBeNull()
    expect(call?.id).toBe('native-call')
    expect(call?.outputFilePaths).toEqual([path])
    expect(JSON.stringify(frame)).toBe(before)
  })
})

describe('native path ownership and preview preservation', () => {
  it.each([null, false, 0, -1, '', [], {}])('refuses a non-native payload without deriving a new call: %j', (payload) => {
    const pathsFor = outputFilePathFixture(AgentProvider.CLAUDE_CODE, frame, options)
    expect(pathsFor(payload)).toEqual([])
  })

  it('refuses another native call while the original call stays fixed', () => {
    const pathsFor = outputFilePathFixture(AgentProvider.CLAUDE_CODE, frame, options)
    const foreign: unknown = JSON.parse(JSON.stringify(frame).replaceAll('native-call', 'foreign-call'))
    expect(pathsFor(foreign)).toEqual([])
  })

  it.each(['request', 'none', 'other'] as const)('supplies no file path for the %s role', (role) => {
    const pathsFor = outputFilePathFixture(AgentProvider.CLAUDE_CODE, frame, options)
    expect(pathsFor(frame, { role })).toEqual([])
  })

  it.each(['file:///native/opaque', 'https://example.com/result', 'zcode-artifact://session/id', ' ', 'relative/output', '/native/zero\0byte'])('refuses a non-filesystem pointer: %j', (invalid) => {
    const payload: unknown = JSON.parse(JSON.stringify(frame).replaceAll(JSON.stringify(path).slice(1, -1), JSON.stringify(invalid).slice(1, -1)))
    const pathsFor = outputFilePathFixture(AgentProvider.CLAUDE_CODE, frame, options)
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
    const meta = providerToolMeta(AgentProvider.CLAUDE_CODE, frame, { ...options, supplementalContent })
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
    const pathsFor = outputFilePathFixture(AgentProvider.CLAUDE_CODE, frame, options)
    expect(pathsFor(frame, { agentSessionId: 'foreign-session' })).toEqual([])
  })
})

describe('native structured and text pointers', () => {
  it('reads explicit metadata without requiring a text wrapper', () => {
    const pathsFor = outputFilePathFixture(AgentProvider.CLAUDE_CODE, frame, options)
    const native = { ...frame, message: { content: [{ ...frame.message.content[0], content: 'native inline preview' }] } }
    expect(pathsFor(native)).toEqual([path])
  })

  it('does not apply a frame-wide metadata path to several native tool results', () => {
    const pathsFor = outputFilePathFixture(AgentProvider.CLAUDE_CODE, frame, options)
    const native = { ...frame, message: { content: [...frame.message.content, { type: 'tool_result', tool_use_id: 'other', content: 'native result' }] } }
    expect(pathsFor(native)).toEqual([])
  })

  it('reads the anchored generic wrapper when structured metadata is absent', () => {
    const pathsFor = outputFilePathFixture(AgentProvider.CLAUDE_CODE, frame, options)
    expect(pathsFor({ ...frame, tool_use_result: undefined })).toEqual([path])
    expect(pathsFor({ ...frame, tool_use_result: undefined, message: { content: [frame.message.content[0], frame.message.content[0]] } })).toEqual([])
  })

  it.each([null, false, 0, -1, '', ' ', 'file:///native/result', '/native/result\0'])('refuses malformed explicit metadata: %j', (persistedOutputPath) => {
    const pathsFor = outputFilePathFixture(AgentProvider.CLAUDE_CODE, frame, options)
    expect(pathsFor({ ...frame, tool_use_result: { ...frame.tool_use_result, persistedOutputPath } })).toEqual([])
  })
})
