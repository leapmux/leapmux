import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { outputFilePathFixture } from '~/test-support/outputFilePathFixture'
import { providerToolCall, providerToolMeta } from '~/test-support/toolCallFixture'
import '../plugin'

const path = '/native/dsh-subprocess-abcdef/dsh-subprocess-123-456-0123456789ab-stdout.log'
const frame = {
  type: 'tool/result',
  seq: 2,
  data: {
    message: {
      toolCallId: 'native-call',
      isError: false,
      content: [
        {
          type: 'text',
          text: 'native inline preview\n[output truncated; full output: /native/dsh-subprocess-abcdef/dsh-subprocess-123-456-0123456789ab-stdout.log]\n[exit code: 0]',
        },
      ],
    },
  },
}
const options = {
  spanId: 'native-call',
  spanType: 'bash',
  agentSessionId: 'native-session',
}

describe('registered output file paths', () => {
  it('attaches the native filesystem pointer without changing the native frame', () => {
    const before = JSON.stringify(frame)
    const call = providerToolCall(AgentProvider.DEEPSEEK_HARNESS, frame, options)

    expect(call).not.toBeNull()
    expect(call?.id).toBe('native-call')
    expect(call?.outputFilePaths).toEqual([path])
    expect(JSON.stringify(frame)).toBe(before)
  })
})

describe('native path ownership and preview preservation', () => {
  it.each([null, false, 0, -1, '', [], {}])('refuses a non-native payload without deriving a new call: %j', (payload) => {
    const pathsFor = outputFilePathFixture(AgentProvider.DEEPSEEK_HARNESS, frame, options)
    expect(pathsFor(payload)).toEqual([])
  })

  it('refuses another native call while the original call stays fixed', () => {
    const pathsFor = outputFilePathFixture(AgentProvider.DEEPSEEK_HARNESS, frame, options)
    const foreign: unknown = JSON.parse(JSON.stringify(frame).replaceAll('native-call', 'foreign-call'))
    expect(pathsFor(foreign)).toEqual([])
  })

  it.each(['request', 'none', 'other'] as const)('supplies no file path for the %s role', (role) => {
    const pathsFor = outputFilePathFixture(AgentProvider.DEEPSEEK_HARNESS, frame, options)
    expect(pathsFor(frame, { role })).toEqual([])
  })

  it.each(['file:///native/opaque', 'https://example.com/result', 'zcode-artifact://session/id', ' ', 'relative/output', '/native/zero\0byte'])('refuses a non-filesystem pointer: %j', (invalid) => {
    const payload: unknown = JSON.parse(JSON.stringify(frame).replaceAll(JSON.stringify(path).slice(1, -1), JSON.stringify(invalid).slice(1, -1)))
    const pathsFor = outputFilePathFixture(AgentProvider.DEEPSEEK_HARNESS, frame, options)
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
    const meta = providerToolMeta(AgentProvider.DEEPSEEK_HARNESS, frame, { ...options, supplementalContent })
    if (!meta)
      throw new Error('The native Copy preview requires a valid tool metadata object.')
    const quote = meta.copyableContent()
    expect(quote).toContain('native inline preview')
    expect(quote).not.toContain(forged)
    expect(JSON.stringify(frame)).toBe(before)
  })
})

describe('native formatted path punctuation', () => {
  it.each([
    '/native/project. notes/dsh-spill/0123456789ab/0123456789ab-bash.txt',
    String.raw`C:\native\project. notes\dsh-spill\0123456789ab\0123456789ab-bash.txt`,
  ])('keeps a period and space inside the declared native path: %j', (nativePath) => {
    const previewText = `(Output omitted. Full formatted result stored at: ${nativePath}. Use read_file.)`
    const native = { ...frame, data: { message: { ...frame.data.message, content: [{ type: 'text', text: previewText }] } } }
    const before = JSON.stringify(native)
    const call = providerToolCall(AgentProvider.DEEPSEEK_HARNESS, native, options)
    expect(call).not.toBeNull()
    expect(call?.outputFilePaths).toEqual([nativePath])
    const pathsFor = outputFilePathFixture(AgentProvider.DEEPSEEK_HARNESS, frame, options)
    expect(pathsFor(native)).toEqual([nativePath])
    const meta = providerToolMeta(AgentProvider.DEEPSEEK_HARNESS, native, options)
    expect(meta?.copyableContent()).toBe(previewText)
    expect(JSON.stringify(native)).toBe(before)
  })
})
