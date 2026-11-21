import { describe, expect, it } from 'vitest'
import { antigravityControlResponseSummary } from './controlResponse'

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
