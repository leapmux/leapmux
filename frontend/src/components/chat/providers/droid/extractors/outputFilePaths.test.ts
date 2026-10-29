import { describe, expect, it } from 'vitest'
import { input } from '~/components/chat/providers/testUtils'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { outputFilePathFixture } from '~/test-support/outputFilePathFixture'
import { providerToolCall, providerToolMeta } from '~/test-support/toolCallFixture'
import '../plugin'

const path = '/native/tmp/droid-terminal-abcdef/58abd506-6e96-404c-88b7-25152c70b972.log'
const frame = {
  type: 'tool_result',
  toolUseId: 'native-call',
  messageId: 'ab8f6d47-df92-4d88-b1fd-ac7cc6e6aec7',
  content: `native inline preview\nFull command output saved to: ${path} (20KB)\n[Process exited with code 0]`,
  isError: false,
}
const options = {
  spanId: 'droid-tool-native-call',
  spanType: 'Execute',
  agentSessionId: 'native-session',
  request: input({
    type: 'tool_call',
    toolUse: {
      id: 'native-call',
      name: 'Execute',
      input: { command: 'printf native-preview' },
    },
    toolName: 'Execute',
  }, undefined, AgentProvider.DROID),
}

describe('registered output file paths', () => {
  it('attaches the native filesystem pointer without changing the native frame', () => {
    const before = JSON.stringify(frame)
    const call = providerToolCall(AgentProvider.DROID, frame, options)

    expect(call).not.toBeNull()
    expect(call?.id).toBe('native-call')
    expect(call?.outputFilePaths).toEqual([path])
    expect(JSON.stringify(frame)).toBe(before)
  })
})

describe('native path ownership and preview preservation', () => {
  it.each([null, false, 0, -1, '', [], {}])('refuses a non-native payload without deriving a new call: %j', (payload) => {
    const pathsFor = outputFilePathFixture(AgentProvider.DROID, frame, options)
    expect(pathsFor(payload)).toEqual([])
  })

  it('refuses another native call while the original call stays fixed', () => {
    const pathsFor = outputFilePathFixture(AgentProvider.DROID, frame, options)
    const foreign: unknown = JSON.parse(JSON.stringify(frame).replaceAll('native-call', 'foreign-call'))
    expect(pathsFor(foreign)).toEqual([])
  })

  it.each(['request', 'none', 'other'] as const)('supplies no file path for the %s role', (role) => {
    const pathsFor = outputFilePathFixture(AgentProvider.DROID, frame, options)
    expect(pathsFor(frame, { role })).toEqual([])
  })

  it.each([
    'file:///native/opaque',
    'https://example.com/result',
    'zcode-artifact://session/id',
    ' ',
    'relative/output',
    '/native/zero\0byte',
  ])('refuses a non-filesystem pointer: %j', (invalid) => {
    const payload: unknown = JSON.parse(JSON.stringify(frame).replaceAll(JSON.stringify(path).slice(1, -1), JSON.stringify(invalid).slice(1, -1)))
    const pathsFor = outputFilePathFixture(AgentProvider.DROID, frame, options)
    expect(pathsFor(payload)).toEqual([])
  })

  it('keeps the native preview when an unrelated supplement supplies complete text', () => {
    const forged = 'FORGED_COMPLETE_BODY'
    const supplementalContent = {
      sessionId: options.agentSessionId,
      toolCallId: 'native-call',
      toolName: options.spanType,
      outputFile: { path, text: forged, output: forged },
      completeOutput: {
        path,
        text: forged,
        sessionId: options.agentSessionId,
        toolCallId: 'native-call',
      },
    }
    const before = JSON.stringify(frame)
    const meta = providerToolMeta(AgentProvider.DROID, frame, { ...options, supplementalContent })
    if (!meta)
      throw new Error('The native Copy preview requires a valid tool metadata object.')
    const quote = meta.copyableContent()
    expect(quote).toContain('native inline preview')
    expect(quote).not.toContain(forged)
    expect(JSON.stringify(frame)).toBe(before)
  })
})

describe('current native truncation notices', () => {
  it('keeps the native middle-cut pointer and original output', () => {
    const content = `head\n\n[... 686415 bytes cut from the middle of 718960; whole output: ${path}]\n\ntail\n\n[Process exited with code 0]`
    const captured = { ...frame, content }
    expect(providerToolCall(AgentProvider.DROID, captured, options)?.outputFilePaths).toEqual([path])
    expect(captured.content).toBe(content)
  })

  it.each([
    '0 bytes cut from the middle of 10',
    '10 bytes cut from the middle of 10',
    '11 bytes cut from the middle of 10',
    '-1 bytes cut from the middle of 10',
  ])('rejects invalid native truncation counts: %s', (counts) => {
    const captured = { ...frame, content: `[... ${counts}; whole output: ${path}]` }
    expect(outputFilePathFixture(AgentProvider.DROID, frame, options)(captured)).toEqual([])
  })

  it('rejects multiple native pointers across both notice forms', () => {
    const captured = { ...frame, content: `${frame.content}\n[... 1 bytes cut from the middle of 2; whole output: ${path}]` }
    expect(outputFilePathFixture(AgentProvider.DROID, frame, options)(captured)).toEqual([])
  })
})
