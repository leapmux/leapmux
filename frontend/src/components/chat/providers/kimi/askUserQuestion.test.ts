import { describe, expect, it } from 'vitest'
import { kimiQuestionRequest } from '~/test-support/kimiFixtures'
import { kimiQuestionRecords, kimiQuestionsFromToolInput } from './askUserQuestion'

const REQUEST = kimiQuestionRequest([
  {
    id: 'q_0',
    question: 'Which color?',
    header: 'Color',
    options: [
      { id: 'opt_0_0', label: 'Red', description: 'Warm' },
      { id: 'opt_0_1', label: 'Blue' },
    ],
    multi_select: false,
    allow_other: true,
  },
  {
    id: 'q_1',
    question: 'Which sizes?',
    header: 'Which sizes?',
    options: [
      { id: 'opt_1_0', label: 'S' },
      { id: 'opt_1_1', label: 'M' },
    ],
    multi_select: true,
    allow_other: true,
  },
  { id: '', question: 'No id' },
  { id: 'q_3', question: '', header: '' },
  {
    id: 'q_4',
    question: 'Broken options',
    options: [
      { id: 'opt_4_0' },
      { label: 'no id' },
      'x',
    ],
  },
])

describe('kimiQuestionRecords', () => {
  it('reads the questions inside a native interaction request', () => {
    const question = {
      id: 'q_0',
      question: 'Which color?',
      options: [
        { id: 'red', label: 'Red' },
      ],
    }
    const payload = {
      type: 'interaction',
      interaction_id: 'question_native',
      session_id: 'session_1',
      agent_id: 'main',
      kind: 'question',
      status: 'pending',
      request: {
        questions: [question],
      },
    }
    expect(kimiQuestionRecords(payload)).toStrictEqual([question])
  })

  it('reads every record in the order the request declares', () => {
    expect(kimiQuestionRecords(REQUEST).map(record => record.id)).toStrictEqual(['q_0', 'q_1', '', 'q_3', 'q_4'])
  })

  it('reads no question from another interaction or from no payload', () => {
    expect(kimiQuestionRecords({ type: 'event.approval.requested' })).toStrictEqual([])
    expect(kimiQuestionRecords({ ...REQUEST, kind: 'approval' })).toStrictEqual([])
    expect(kimiQuestionRecords({})).toStrictEqual([])
  })

  it('skips a record that is not an object, and a question list that is not a list', () => {
    expect(kimiQuestionRecords(kimiQuestionRequest(['not a question', null, 7] as never)))
      .toStrictEqual([])
    expect(kimiQuestionRecords({
      ...kimiQuestionRequest([]),
      request: {
        questions: { id: 'q_0', question: 'Q' },
      },
    })).toStrictEqual([])
  })
})

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
