import { describe, expect, it } from 'vitest'
import { zcodeQuestionsFromToolInput, zcodeQuestionText } from './askUserQuestion'

/**
 * The text that one question shows: the question, or the header of a record that
 * states only that. An empty text marks a record that asks nothing.
 */
describe('zcodeQuestionText', () => {
  it('reads the question before the header', () => {
    expect(zcodeQuestionText({ question: 'Pick any', header: 'Databases' })).toBe('Pick any')
  })

  it('uses the header as the question text when only the header is present', () => {
    expect(zcodeQuestionText({ header: 'Databases' })).toBe('Databases')
  })

  it('answers an empty text for a record with neither text nor header', () => {
    expect(zcodeQuestionText({ options: [] })).toBe('')
  })
})

/**
 * The question list a tool ROW states, in ZCode's own key spellings.
 *
 * `questionsFromRecords` holds the control flow and the two invariants; the header
 * standing in for an absent question, and `value` standing in for an absent label, are
 * what stays here.
 */
describe('zcodeQuestionsFromToolInput', () => {
  it('reads the question, the header and every detail one option carries', () => {
    expect(zcodeQuestionsFromToolInput({
      questions: [
        {
          question: 'Pick a color.',
          header: 'Color',
          options: [
            {
              label: 'Blue',
              value: 'Blue',
              description: 'The color blue.',
              preview: '#0000ff',
            },
          ],
        },
      ],
    })).toStrictEqual([
      {
        header: 'Color',
        question: 'Pick a color.',
        options: [
          { label: 'Blue', description: 'The color blue.', preview: '#0000ff' },
        ],
      },
    ])
  })

  // ZCode's wire form sets an option's `value` to its own label, so either field
  // standing in for the other keeps the option readable.
  it('labels an option with its value when it states no label', () => {
    expect(zcodeQuestionsFromToolInput({
      questions: [
        {
          question: 'Pick a color.',
          options: [
            { value: 'Blue' },
          ],
        },
      ],
    })[0]?.options)
      .toStrictEqual([
        { label: 'Blue' },
      ])
  })

  // The shared control keys the answer by the text it shows, and a header alone is that
  // text for a record the app-server sent without a question.
  it('reads the header as the text of a question that states none', () => {
    expect(zcodeQuestionsFromToolInput({
      questions: [
        { header: 'Color', options: [] },
      ],
    })[0]?.question).toBe('Color')
  })

  it('drops a question that states neither a question nor a header', () => {
    expect(zcodeQuestionsFromToolInput({
      questions: [
        {
          options: [
            { label: 'Blue' },
          ],
        },
      ],
    })).toStrictEqual([])
  })

  it('drops an option that states neither a label nor a value', () => {
    expect(zcodeQuestionsFromToolInput({
      questions: [
        {
          question: 'Pick a color.',
          options: [
            { description: 'The color blue.' },
          ],
        },
      ],
    })[0]?.options)
      .toStrictEqual([])
  })
})
