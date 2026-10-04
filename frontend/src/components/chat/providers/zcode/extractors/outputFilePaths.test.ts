import { describe, expect, it } from 'vitest'
import { ZCODE_TOOL_PREFIX } from '~/generated/contracts/zcode-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { outputFilePathFixture } from '~/test-support/outputFilePathFixture'
import { providerToolCall, providerToolMeta } from '~/test-support/toolCallFixture'
import '../plugin'

const path = '/native/zcode/artifacts/native-session/native-call-tool-result-11111111-1111-4111-8111-111111111111.txt'
const frame = {
  type: 'tool.updated',
  sessionId: 'native-session',
  payload: {
    kind: 'result',
    toolCallId: 'native-call',
    result: {
      success: true,
      content: 'native inline preview',
      truncated: true,
    },
  },
}
const options = {
  spanId: 'native-call',
  spanType: 'Bash',
  agentSessionId: 'native-session',
  supplementalContent: {
    type: 'tool.updated',
    payload: {
      kind: 'result',
      toolCallId: 'native-call',
    },
    nativeTool: {
      id: 'part-native',
      sessionId: 'native-session',
      messageId: 'message-native',
      data: {
        type: 'tool',
        callID: 'native-call',
        tool: 'Bash',
        state: {
          status: 'completed',
          metadata: {
            serialization: {
              budgetStrategy: 'artifact',
              artifactPath: '/native/zcode/artifacts/native-session/native-call-tool-result-11111111-1111-4111-8111-111111111111.txt',
              truncated: true,
              originalBytes: 20000,
            },
          },
        },
      },
    },
  },
}

describe('registered output file paths', () => {
  it('attaches the native filesystem pointer without changing the native frame', () => {
    const before = JSON.stringify(frame)
    const call = providerToolCall(AgentProvider.ZCODE, frame, options)

    expect(call).not.toBeNull()
    expect(call?.id).toBe('native-call')
    expect(call?.outputFilePaths).toEqual([path])
    expect(JSON.stringify(frame)).toBe(before)
  })
})

describe('native path ownership and preview preservation', () => {
  it.each([null, false, 0, -1, '', [], {}])('refuses a non-native payload without deriving a new call: %j', (payload) => {
    const pathsFor = outputFilePathFixture(AgentProvider.ZCODE, frame, options)
    expect(pathsFor(payload)).toEqual([])
  })

  it('refuses another native call while the original call stays fixed', () => {
    const pathsFor = outputFilePathFixture(AgentProvider.ZCODE, frame, options)
    const foreign: unknown = JSON.parse(JSON.stringify(frame).replaceAll('native-call', 'foreign-call'))
    expect(pathsFor(foreign)).toEqual([])
  })

  it.each(['request', 'none', 'other'] as const)('supplies no file path for the %s role', (role) => {
    const pathsFor = outputFilePathFixture(AgentProvider.ZCODE, frame, options)
    expect(pathsFor(frame, { role })).toEqual([])
  })

  it.each(['file:///native/opaque', 'https://example.com/result', 'zcode-artifact://session/id', ' ', 'relative/output', '/native/zero\0byte'])('refuses a non-filesystem pointer: %j', (invalid) => {
    const payload: unknown = JSON.parse(JSON.stringify(frame).replaceAll(JSON.stringify(path).slice(1, -1), JSON.stringify(invalid).slice(1, -1)))
    const supplemental: unknown = options.supplementalContent === undefined ? undefined : JSON.parse(JSON.stringify(options.supplementalContent).replaceAll(JSON.stringify(path).slice(1, -1), JSON.stringify(invalid).slice(1, -1)))
    const pathsFor = outputFilePathFixture(AgentProvider.ZCODE, frame, options)
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
    const meta = providerToolMeta(AgentProvider.ZCODE, frame, { ...options, supplementalContent })
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
    const pathsFor = outputFilePathFixture(AgentProvider.ZCODE, frame, options)
    expect(pathsFor(frame, { agentSessionId: 'foreign-session' })).toEqual([])
  })
})

describe('parent Worker and child native session ownership', () => {
  it('keeps an explicitly owned child pointer while the Worker event belongs to its parent', () => {
    const childCallId = `${ZCODE_TOOL_PREFIX.Subagent}agent-child_native-call`
    const childPath = path.replace('/native-session/', '/child-session/')
    const child = { ...frame, payload: { ...frame.payload, toolCallId: childCallId, agentId: 'agent-child', childSessionId: 'child-session' } }
    const supplementalContent = {
      ...options.supplementalContent,
      payload: { ...options.supplementalContent.payload, toolCallId: childCallId },
      nativeTool: { ...options.supplementalContent.nativeTool, sessionId: 'child-session', data: { ...options.supplementalContent.nativeTool.data, state: { ...options.supplementalContent.nativeTool.data.state, metadata: { serialization: { ...options.supplementalContent.nativeTool.data.state.metadata.serialization, artifactPath: childPath } } } } },
    }
    const childOptions = { ...options, spanId: childCallId, supplementalContent }
    const pathsFor = outputFilePathFixture(AgentProvider.ZCODE, child, childOptions)
    expect(pathsFor()).toEqual([childPath])
    expect(pathsFor(child, { agentSessionId: 'foreign-parent' })).toEqual([])
    expect(pathsFor({ ...child, payload: { ...child.payload, childSessionId: 'foreign-child' } })).toEqual([])
  })
})
