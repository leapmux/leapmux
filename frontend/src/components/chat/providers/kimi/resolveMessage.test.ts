import { describe, expect, it } from 'vitest'
import { KIMI_CONVERSATION_MEDIA } from '~/generated/contracts/kimi-protocol'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { kimiConversationToolRecord } from '~/test-support/kimiFixtures'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { resolveKimiMessage } from './resolveMessage'
import '../index'

const FILE_ID = `f_${'a'.repeat(64)}`
const DATA = 'aW1hZ2U='

function mediaResult() {
  return kimiConversationToolRecord('result', {
    item_id: 'call_1',
    name: 'ReadMediaFile',
    status: 'ended',
    outcome: 'completed',
    input: { path: 'image.png' },
    media: [
      { kind: 'image', file_id: FILE_ID },
    ],
  })
}

function mediaReceipt() {
  return {
    session_id: 'session_1',
    agent_id: 'main',
    turn_id: 't0',
    item_id: 'call_1',
    files: {
      [FILE_ID]: { [KIMI_CONVERSATION_MEDIA.MIMEType]: 'image/png', data: DATA },
    },
  }
}

function expectNoResolvedImages(receipt: unknown, agentSessionId: string | undefined) {
  const call = providerToolCall(AgentProvider.KIMI_CODE, mediaResult(), {
    ...(agentSessionId === undefined
      ? {}
      : { agentSessionId }),
    supplementalContent: { kimi_media: receipt },
  })
  expect(call?.images).toStrictEqual([])
}

describe('resolveKimiMessage', () => {
  it.each(['', 'native output'])('preserves explicit output %j against a foreign correction', (output) => {
    const result = kimiConversationToolRecord('result', {
      name: 'Bash',
      status: 'ended',
      outcome: 'completed',
      input: { command: 'printf output' },
      output,
    })
    const foreign = {
      ...result,
      turn_id: 'foreign',
      item: { ...result.item, output: 'collected tail' },
    }
    expect(resolveKimiMessage({
      rawText: '',
      topLevel: result,
      parentObject: result,
      wrapper: null,
      agentSessionId: 'session_1',
      supplementalContent: { kimi_conversation: foreign },
    })).toBeUndefined()
  })

  it('restores collected output only for a correction of the owning stopped tool', () => {
    const frame = kimiConversationToolRecord('result', {
      name: 'Bash',
      input: { command: 'printf output' },
    })
    const revision = {
      ...frame,
      item: { ...frame.item, output: 'COLLECTED-TOOL-TAIL' },
      output_truncated: true,
    }
    const options = {
      agentSessionId: 'session_1',
      completion: MessageCompletion.INTERRUPTED,
      supplementalContent: { kimi_conversation: revision },
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
        kimi_conversation: {
          ...revision,
          item: { ...revision.item, item_id: 'foreign' },
        },
      },
    })
    expect(foreign?.result).toBeUndefined()
  })

  it('restores native image formats outside the attachment input allowlist', () => {
    const data = 'Qk06AAAAAAAAADYAAAAoAAAAAQAAAAEAAAABABgAAAAAAAQAAAATCwAAEwsAAAAAAAAAAAAAAAD/AA=='
    const call = providerToolCall(AgentProvider.KIMI_CODE, mediaResult(), {
      agentSessionId: 'session_1',
      supplementalContent: {
        kimi_media: {
          ...mediaReceipt(),
          files: {
            [FILE_ID]: { [KIMI_CONVERSATION_MEDIA.MIMEType]: 'image/bmp', data },
          },
        },
      },
    })
    expect(call?.images).toStrictEqual([
      { mimeType: 'image/bmp', data },
    ])
  })

  it.each([
    'session_id',
    'agent_id',
    'turn_id',
    'item_id',
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
    { [KIMI_CONVERSATION_MEDIA.MIMEType]: 'text/plain', data: DATA },
    { [KIMI_CONVERSATION_MEDIA.MIMEType]: '', data: DATA },
    { [KIMI_CONVERSATION_MEDIA.MIMEType]: 'image/', data: DATA },
    { [KIMI_CONVERSATION_MEDIA.MIMEType]: 'image/png; charset=utf-8', data: DATA },
    { [KIMI_CONVERSATION_MEDIA.MIMEType]: 'image/png', data: '' },
    { [KIMI_CONVERSATION_MEDIA.MIMEType]: 'image/png', data: 'not base64' },
    { [KIMI_CONVERSATION_MEDIA.MIMEType]: 'image/png', data: 'aW1hZ2V=' },
    { [KIMI_CONVERSATION_MEDIA.MIMEType]: 'image/png' },
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
        [`f_${'b'.repeat(64)}`]: { [KIMI_CONVERSATION_MEDIA.MIMEType]: 'image/png', data: DATA },
      },
    }
    expectNoResolvedImages(receipt, 'session_1')
  })
})
