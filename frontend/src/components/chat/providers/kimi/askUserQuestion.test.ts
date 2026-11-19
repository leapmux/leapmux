import { describe, expect, it } from 'vitest'
import { kimiQuestionsFromToolInput } from './askUserQuestion'

describe('kimiQuestionsFromToolInput', () => {
  it('reads the words of the call arguments', () => {
    expect(kimiQuestionsFromToolInput({
      questions: [
        {
          question: 'Which?',
          header: 'H',
          options: [
            { label: 'A', description: 'a' },
            {},
          ],
        },
        { header: 'no question' },
      ],
    }))
      .toStrictEqual([
        {
          header: 'H',
          question: 'Which?',
          options: [
            { label: 'A', description: 'a' },
          ],
        },
      ])
    expect(kimiQuestionsFromToolInput({})).toStrictEqual([])
  })
})
