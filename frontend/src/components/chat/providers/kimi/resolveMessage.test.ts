import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { kimiTranscriptToolFrame } from '~/test-support/kimiFixtures'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { resolveKimiMessage } from './resolveMessage'
import '../index'

const FILE_ID = `f_${'a'.repeat(64)}`
const DATA = 'aW1hZ2U='

function mediaResult() {
  return kimiTranscriptToolFrame({
    name: 'ReadMediaFile',
    state: 'done',
    input: { path: 'image.png' },
    output: [
      {
        type: 'image',
        source: { kind: 'session_media', file_id: FILE_ID },
      },
    ],
  })
}

function mediaReceipt() {
  return {
    sessionId: 'session_1',
    agentId: 'main',
    turnId: 't0',
    stepId: 't0.1',
    frameId: 't0.1.call_1',
    toolCallId: 'call_1',
    files: {
      [FILE_ID]: { mimeType: 'image/png', data: DATA },
    },
  }
}

function expectNoResolvedImages(receipt: unknown, agentSessionId: string | undefined) {
  const call = providerToolCall(AgentProvider.KIMI_CODE, mediaResult(), {
    ...(agentSessionId === undefined
      ? {}
      : { agentSessionId }),
    supplementalContent: { media: receipt },
  })
  expect(call?.images).toStrictEqual([])
}

describe('resolveKimiMessage', () => {
  it.each([null, '', 0, false, 'native output'])('preserves an explicit native output %j', (output) => {
    const frame = kimiTranscriptToolFrame({
      name: 'Bash',
      state: 'done',
      input: { command: 'printf output' },
      output,
    })
    const receipt = {
      ...mediaReceipt(),
      text: 'collected tail',
      truncated: true,
    }
    expect(resolveKimiMessage({
      rawText: '',
      topLevel: frame,
      parentObject: frame,
      wrapper: null,
      agentSessionId: 'session_1',
      supplementalContent: { toolOutput: receipt },
    })).toBeUndefined()
  })

  it('restores collected native output only for the owning stopped tool', () => {
    const frame = kimiTranscriptToolFrame({
      name: 'Bash',
      state: 'running',
      input: { command: 'printf output' },
    })
    const toolOutput = {
      sessionId: 'session_1',
      agentId: 'main',
      turnId: 't0',
      stepId: 't0.1',
      frameId: 't0.1.call_1',
      toolCallId: 'call_1',
      text: 'COLLECTED-TOOL-TAIL',
      truncated: true,
    }
    const options = {
      agentSessionId: 'session_1',
      completion: MessageCompletion.INTERRUPTED,
      supplementalContent: { toolOutput },
    }
    const before = structuredClone(frame)
    const call = providerToolCall(AgentProvider.KIMI_CODE, frame, options)
    expect(call?.result).toStrictEqual({
      commands: [
        { output: 'COLLECTED-TOOL-TAIL' },
      ],
      unresolvedTerminals: [],
    })
    expect(call?.truncated).toBe(true)
    expect(frame).toStrictEqual(before)
    const foreign = providerToolCall(AgentProvider.KIMI_CODE, frame, {
      ...options,
      supplementalContent: {
        toolOutput: { ...toolOutput, frameId: 'foreign' },
      },
    })
    expect(foreign?.result).toBeUndefined()
  })

  it('restores native image formats outside the attachment input allowlist', () => {
    const data = 'Qk06AAAAAAAAADYAAAAoAAAAAQAAAAEAAAABABgAAAAAAAQAAAATCwAAEwsAAAAAAAAAAAAAAAD/AA=='
    const call = providerToolCall(AgentProvider.KIMI_CODE, mediaResult(), {
      agentSessionId: 'session_1',
      supplementalContent: {
        media: {
          ...mediaReceipt(),
          files: {
            [FILE_ID]: { mimeType: 'image/bmp', data },
          },
        },
      },
    })
    expect(call?.images).toStrictEqual([
      { mimeType: 'image/bmp', data },
    ])
  })

  it.each([
    'sessionId',
    'agentId',
    'turnId',
    'stepId',
    'frameId',
    'toolCallId',
  ] as const)('rejects a mismatched %s', (field) => {
    const receipt = {
      ...mediaReceipt(),
      [field]: 'foreign',
    }
    expectNoResolvedImages(receipt, 'session_1')
  })

  it.each([undefined, '', 'foreign'])('rejects an absent or foreign stored session %s', (agentSessionId) => {
    expectNoResolvedImages(mediaReceipt(), agentSessionId)
  })

  it.each([
    { mimeType: 'text/plain', data: DATA },
    { mimeType: '', data: DATA },
    { mimeType: 'image/', data: DATA },
    { mimeType: 'image/png; charset=utf-8', data: DATA },
    { mimeType: 'image/png', data: '' },
    { mimeType: 'image/png', data: 'not base64' },
    { mimeType: 'image/png', data: 'aW1hZ2V=' },
    { mimeType: 'image/png' },
    null,
  ])('rejects invalid image bytes or MIME metadata %j', (file) => {
    const receipt = {
      ...mediaReceipt(),
      files: { [FILE_ID]: file },
    }
    expectNoResolvedImages(receipt, 'session_1')
  })

  it('does not attach an unreferenced image', () => {
    const receipt = {
      ...mediaReceipt(),
      files: {
        [`f_${'b'.repeat(64)}`]: { mimeType: 'image/png', data: DATA },
      },
    }
    expectNoResolvedImages(receipt, 'session_1')
  })
})
