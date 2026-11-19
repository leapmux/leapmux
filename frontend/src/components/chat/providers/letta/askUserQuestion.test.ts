import { describe, expect, it } from 'vitest'
import { lettaQuestionReceiptMessage } from './askUserQuestion'

describe('lettaQuestionReceiptMessage', () => {
  // The receipt of a live Letta Code 0.34.2 run, verbatim.
  const MESSAGE = 'Questions posted. Answers or dismissal will arrive later in a task notification. You may continue working; do not assume an answer.'
  const RECEIPT = {
    type: 'ask_user_question',
    version: 2,
    toolCallId: 'ask-1',
    questions: [
      {
        question: 'Which color do you prefer?',
        header: 'Color',
        options: [
          { label: 'Blue', description: 'The color blue' },
          { label: 'Red', description: 'The color red' },
        ],
      },
    ],
    message: MESSAGE,
  }

  it('reads the message of a receipt', () => {
    expect(lettaQuestionReceiptMessage(RECEIPT)).toBe(MESSAGE)
  })

  it.each([
    ['no object', null],
    [
      'an object with no type',
      { message: 'Successfully replaced 1 occurrence' },
    ],
    [
      'a response, not a receipt',
      { ...RECEIPT, type: 'ask_user_question_response' },
    ],
    [
      'a receipt with no message',
      { ...RECEIPT, message: undefined },
    ],
    [
      'a receipt with an empty message',
      { ...RECEIPT, message: '' },
    ],
    [
      'a receipt with a blank message',
      { ...RECEIPT, message: '  \n' },
    ],
    [
      'a receipt with a message that is no text',
      { ...RECEIPT, message: 7 },
    ],
  ])('reads no message from %s', (_name, returned) => {
    expect(lettaQuestionReceiptMessage(returned)).toBeUndefined()
  })
})
