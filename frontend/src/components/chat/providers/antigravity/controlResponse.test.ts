import { describe, expect, it, vi } from 'vitest'
import { createControlAnswerState } from '../../controls/types'
import { antigravityExtractQuestions } from './askUserQuestion'
import { antigravityControlResponseSummary, sendAntigravityQuestionResponse } from './controlResponse'

describe('antigravityControlResponseSummary', () => {
  it('shows the selected option label and the custom answer in the saved response row', () => {
    const color = {
      question: 'Which color?',
      options: [
        { id: 'a', text: 'Blue' },
        { id: 'b', text: 'Green' },
      ],
    }
    const reason = { question: 'Why?' }
    expect(antigravityControlResponseSummary({
      requestId: 'question',
      claimToken: '',
      request: {
        requestedInteraction: {
          askQuestion: {
            questions: [color, reason],
          },
        },
      },
      response: {
        interaction: {
          askQuestion: {
            responses: [
              {
                ...color,
                selectedOptionIds: ['b'],
              },
              { ...reason, writeInResponse: 'It matches.' },
            ],
          },
        },
      },
    })).toEqual({ kind: 'label', text: 'Which color?: Green\nWhy?: It matches.' })
  })
})

describe('sendAntigravityQuestionResponse', () => {
  it('keeps native question positions when an invalid record is skipped', async () => {
    const first = { id: 'first', question: 'First?' }
    const third = { id: 'third', question: 'Third?' }
    const request = {
      agentId: 'agent',
      requestId: 'antigravity:cascade:trajectory:0',
      payload: {
        requestedInteraction: {
          askQuestion: {
            questions: [first, null, third],
          },
        },
      },
    }
    const onRespond = vi.fn(async (_response: Uint8Array) => {})
    const answers = createControlAnswerState({
      customTexts: { 1: 'Third answer' },
    })
    await sendAntigravityQuestionResponse(request, onRespond, antigravityExtractQuestions(request.payload), answers)
    const response = JSON.parse(new TextDecoder().decode(onRespond.mock.calls[0]![0]))
    expect(response.interaction.askQuestion.responses).toEqual([
      { ...first, skipped: true },
      { skipped: true },
      { ...third, writeInResponse: 'Third answer' },
    ])
  })
})
