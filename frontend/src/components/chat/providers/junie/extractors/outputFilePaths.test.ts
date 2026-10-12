import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { outputFilePathFixture } from '~/test-support/outputFilePathFixture'
import { providerToolCall, providerToolMeta } from '~/test-support/toolCallFixture'
import '../plugin'

const path = '/native/junie/sessions/session-261004-010203-abcd/task-261004-010204-efgh/terminal-output/terminal-output-123.txt'
const frame = {
  sessionUpdate: 'tool_call_update',
  toolCallId: 'native-call',
  status: 'completed',
  kind: 'execute',
  title: 'native command',
  rawInput: {
    command: 'printf native-preview',
    cwd: '/native/project',
  },
  content: [],
  rawOutput: {
    output: 'native inline preview',
  },
  _meta: {
    terminal_exit: {
      terminal_id: 'native-call',
      exit_code: 0,
      signal: null,
    },
  },
}
const options = {
  spanId: 'native-call',
  spanType: 'execute',
  agentSessionId: 'session-261004-010203-abcd',
  supplementalContent: {
    sessionUpdate: 'tool_call_update',
    toolCallId: 'native-call',
    status: 'completed',
    outputFilePath: {
      sessionId: 'session-261004-010203-abcd',
      toolCallId: 'native-call',
      taskId: 'task-261004-010204-efgh',
      command: 'printf native-preview',
      cwd: '/native/project',
      path: '/native/junie/sessions/session-261004-010203-abcd/task-261004-010204-efgh/terminal-output/terminal-output-123.txt',
      exitCode: 0,
    },
  },
}

describe('registered output file paths', () => {
  it('attaches the native filesystem pointer without changing the native frame', () => {
    const before = JSON.stringify(frame)
    const call = providerToolCall(AgentProvider.JUNIE, frame, options)

    expect(call).not.toBeNull()
    expect(call?.id).toBe('native-call')
    expect(call?.outputFilePaths).toEqual([path])
    expect(JSON.stringify(frame)).toBe(before)
  })
})

describe('native path ownership and preview preservation', () => {
  it.each([null, false, 0, -1, '', [], {}])('refuses a non-native payload without deriving a new call: %j', (payload) => {
    const pathsFor = outputFilePathFixture(AgentProvider.JUNIE, frame, options)
    expect(pathsFor(payload)).toEqual([])
  })

  it('refuses another native call while the original call stays fixed', () => {
    const pathsFor = outputFilePathFixture(AgentProvider.JUNIE, frame, options)
    const foreign: unknown = JSON.parse(JSON.stringify(frame).replaceAll('native-call', 'foreign-call'))
    expect(pathsFor(foreign)).toEqual([])
  })

  it.each(['request', 'none', 'other'] as const)('supplies no file path for the %s role', (role) => {
    const pathsFor = outputFilePathFixture(AgentProvider.JUNIE, frame, options)
    expect(pathsFor(frame, { role })).toEqual([])
  })

  it.each(['file:///native/opaque', 'https://example.com/result', 'zcode-artifact://session/id', ' ', 'relative/output', '/native/zero\0byte'])('refuses a non-filesystem pointer: %j', (invalid) => {
    const payload: unknown = JSON.parse(JSON.stringify(frame).replaceAll(JSON.stringify(path).slice(1, -1), JSON.stringify(invalid).slice(1, -1)))
    const supplemental: unknown = options.supplementalContent === undefined ? undefined : JSON.parse(JSON.stringify(options.supplementalContent).replaceAll(JSON.stringify(path).slice(1, -1), JSON.stringify(invalid).slice(1, -1)))
    const pathsFor = outputFilePathFixture(AgentProvider.JUNIE, frame, options)
    expect(pathsFor(payload, { supplementalContent: supplemental })).toEqual([])
  })

  it('keeps the native preview when an unrelated supplement supplies complete text', () => {
    const forged = 'FORGED_COMPLETE_BODY'
    const supplementalContent = {
      ...options.supplementalContent,
      sessionId: options.agentSessionId,
      toolCallId: 'native-call',
      toolName: options.spanType,
      outputFile: { path, text: forged, output: forged },
      completeOutput: { path, text: forged, sessionId: options.agentSessionId, toolCallId: 'native-call' },
    }
    const before = JSON.stringify(frame)
    const meta = providerToolMeta(AgentProvider.JUNIE, frame, { ...options, supplementalContent })
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
    const pathsFor = outputFilePathFixture(AgentProvider.JUNIE, frame, options)
    expect(pathsFor(frame, { agentSessionId: 'foreign-session' })).toEqual([])
  })
})

describe('pointer-only native receipt', () => {
  it.each([
    { sessionId: 'foreign' },
    { toolCallId: 'foreign' },
    { command: 'another command' },
    { cwd: '/another/project' },
    { taskId: 'task-nested/child' },
    { exitCode: 1 },
    { exitCode: undefined },
  ])('refuses another receipt owner or an absent exit code: %j', (change) => {
    const pathsFor = outputFilePathFixture(AgentProvider.JUNIE, frame, options)
    expect(pathsFor(frame, { supplementalContent: { ...options.supplementalContent, outputFilePath: { ...options.supplementalContent.outputFilePath, ...change } } })).toEqual([])
  })

  it('preserves empty and Unicode native previews without storing them in the receipt', () => {
    const pathsFor = outputFilePathFixture(AgentProvider.JUNIE, frame, options)
    for (const output of ['', '文😀\nzero:false']) {
      expect(pathsFor({ ...frame, rawOutput: { output } })).toEqual([path])
    }
    expect(options.supplementalContent.outputFilePath).not.toHaveProperty('output')
    expect(options.supplementalContent.outputFilePath).not.toHaveProperty('nativeOutput')
  })

  it('refuses a missing native exit and a conflicting reported raw exit', () => {
    const pathsFor = outputFilePathFixture(AgentProvider.JUNIE, frame, options)
    expect(pathsFor({ ...frame, _meta: {} })).toEqual([])
    expect(pathsFor({ ...frame, rawOutput: { ...frame.rawOutput, exitCode: 1 } })).toEqual([])
    expect(pathsFor(frame, { supplementalContent: undefined })).toEqual([])
  })
})
