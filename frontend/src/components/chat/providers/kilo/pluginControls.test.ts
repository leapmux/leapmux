import { describe, expect, it, vi } from 'vitest'
import { KILO_AUDIO_MIME } from '~/generated/contracts/kilo-protocol'
import { NATIVE_QUESTION_EVENT } from '~/generated/contracts/native-questions'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { createControlAnswerState } from '../../controls/types'
import { providerFor } from '../registry'
import './plugin'

const plugin = providerFor(AgentProvider.KILO)!

describe('kiloQuestionHandling', () => {
  it.each(['0', '007', 'kilo-question:que_native'])('sends ordered answers through the registered handler with request ID %s', async (requestId) => {
    const packets: unknown[] = []
    const send = vi.fn(async (bytes: Uint8Array) => {
      packets.push(JSON.parse(new TextDecoder().decode(bytes)))
    })
    const payload = {
      type: NATIVE_QUESTION_EVENT.QuestionAsked,
      properties: {
        questions: [
          {
            header: 'Action',
            question: 'Choose an action',
            options: [
              { label: 'Build' },
            ],
          },
          { header: 'Environment', question: 'Choose an environment', options: [] },
        ],
      },
    }
    const handler = plugin.controls?.askUserQuestion
    expect(handler?.isRequest(payload)).toBe(true)
    const questions = handler?.extractQuestions(payload) ?? []
    expect(questions).toHaveLength(2)
    await handler?.sendAnswer({ requestId, agentId: 'agent-1', payload }, send, questions, createControlAnswerState({
      selections: {
        0: ['Build'],
        1: [],
      },
      customTexts: { 0: 'ignored when selected', 1: '  Development  ' },
    }))
    expect(packets).toEqual([
      {
        jsonrpc: '2.0',
        id: requestId,
        result: {
          answers: [
            ['Build'],
            ['Development'],
          ],
        },
      },
    ])
    expect(send).toHaveBeenCalledOnce()
  })

  it('rejects through the registered handler with the original request ID', async () => {
    const packets: unknown[] = []
    const send = async (bytes: Uint8Array) => {
      packets.push(JSON.parse(new TextDecoder().decode(bytes)))
    }
    await plugin.controls?.askUserQuestion?.sendReject({ requestId: '007', agentId: 'agent-1', payload: {} }, send, '')
    expect(packets).toEqual([
      {
        jsonrpc: '2.0',
        id: '007',
        result: { rejected: true },
      },
    ])
  })

  it.each([
    undefined,
    null,
    '',
    0,
    false,
    {},
    [null, 'invalid'],
  ])('keeps malformed question lists empty: %j', (questions) => {
    expect(plugin.controls?.askUserQuestion?.extractQuestions({
      properties: { questions },
    })).toEqual([])
  })

  it('recognizes only the native asked event as a question request', () => {
    expect(plugin.controls?.askUserQuestion?.isRequest({ type: NATIVE_QUESTION_EVENT.QuestionReplied })).toBe(false)
    expect(plugin.controls?.askUserQuestion?.isRequest({ type: 'unrelated' })).toBe(false)
  })

  it('uses the generated Kilo audio capability independently of OpenCode', () => {
    expect(plugin.configuration?.attachments?.audio).toEqual([...KILO_AUDIO_MIME])
  })
})
