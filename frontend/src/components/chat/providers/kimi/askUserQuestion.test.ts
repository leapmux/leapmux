import type { ControlQuestion } from '../../model/question'
import { describe, expect, it } from 'vitest'
import { createControlAnswerState } from '~/components/chat/controls/types'
import { kimiQuestionRequest } from '~/test-support/kimiFixtures'
import { buildKimiAnswers, kimiAnswer, kimiIsQuestionRequest, kimiQuestionsFromPayload, kimiQuestionsFromToolInput } from './askUserQuestion'

const REQUEST = kimiQuestionRequest([
  { id: 'q_0', question: 'Which color?', header: 'Color', options: [{ id: 'opt_0_0', label: 'Red', description: 'Warm' }, { id: 'opt_0_1', label: 'Blue' }], multi_select: false, allow_other: true },
  { id: 'q_1', question: 'Which sizes?', header: 'Which sizes?', options: [{ id: 'opt_1_0', label: 'S' }, { id: 'opt_1_1', label: 'M' }], multi_select: true, allow_other: true },
  { id: '', question: 'No id' },
  { id: 'q_3', question: '', header: '' },
  { id: 'q_4', question: 'Broken options', options: [{ id: 'opt_4_0' }, { label: 'no id' }, 'x'] },
])

describe('kimiQuestionsFromPayload', () => {
  it('reads each question with its option ids as values', () => {
    expect(kimiQuestionsFromPayload(REQUEST)).toStrictEqual([
      { id: 'q_0', question: 'Which color?', header: 'Color', options: [{ value: 'opt_0_0', label: 'Red', description: 'Warm' }, { value: 'opt_0_1', label: 'Blue' }], allowEmpty: true },
      { id: 'q_1', question: 'Which sizes?', options: [{ value: 'opt_1_0', label: 'S' }, { value: 'opt_1_1', label: 'M' }], allowEmpty: true, multiSelect: true },
      { id: 'q_4', question: 'Broken options', options: [], allowEmpty: true },
    ])
  })

  it('recognizes a question request', () => {
    expect(kimiIsQuestionRequest(REQUEST)).toBe(true)
    expect(kimiIsQuestionRequest({ type: 'event.approval.requested' })).toBe(false)
    expect(kimiIsQuestionRequest({})).toBe(false)
    expect(kimiQuestionsFromPayload({})).toStrictEqual([])
  })

  it('reads the header as the question of a record that states only a header', () => {
    expect(kimiQuestionsFromPayload(kimiQuestionRequest([{ id: 'q_0', question: '', header: 'Pick a color' }])))
      .toStrictEqual([{ id: 'q_0', question: 'Pick a color', options: [], allowEmpty: true }])
  })

  it('skips a record that is not an object, and a question list that is not a list', () => {
    expect(kimiQuestionsFromPayload(kimiQuestionRequest(['not a question', null, 7] as never)))
      .toStrictEqual([])
    expect(kimiQuestionsFromPayload({ ...kimiQuestionRequest([]), questions: { id: 'q_0', question: 'Q' } })).toStrictEqual([])
  })

  it('reads only a literal true as a multiple-choice question', () => {
    const [question] = kimiQuestionsFromPayload(kimiQuestionRequest([{ id: 'q_0', question: 'Q', multi_select: 'true' }]))
    expect(question?.multiSelect).toBeUndefined()
  })
})

describe('kimiQuestionsFromToolInput', () => {
  it('reads the words of the call arguments', () => {
    expect(kimiQuestionsFromToolInput({ questions: [{ question: 'Which?', header: 'H', options: [{ label: 'A', description: 'a' }, {}] }, { header: 'no question' }] }))
      .toStrictEqual([{ header: 'H', question: 'Which?', options: [{ label: 'A', description: 'a' }] }])
    expect(kimiQuestionsFromToolInput({})).toStrictEqual([])
  })
})

describe('kimiAnswer', () => {
  const single: ControlQuestion = { id: 'q_0', question: 'Q', options: [{ value: 'a', label: 'A' }, { value: 'b', label: 'B' }] }
  const multi: ControlQuestion = { ...single, multiSelect: true }

  it('answers each kind the server takes', () => {
    expect(kimiAnswer(single, ['a'], '')).toStrictEqual({ kind: 'single', option_id: 'a' })
    expect(kimiAnswer(single, [], ' Green ')).toStrictEqual({ kind: 'other', text: 'Green' })
    expect(kimiAnswer(multi, ['a', 'b'], '')).toStrictEqual({ kind: 'multi', option_ids: ['a', 'b'] })
    expect(kimiAnswer(multi, ['a'], 'c')).toStrictEqual({ kind: 'multi_with_other', option_ids: ['a'], other_text: 'c' })
    expect(kimiAnswer(multi, [], 'c')).toStrictEqual({ kind: 'other', text: 'c' })
    expect(kimiAnswer(single, [], '  ')).toStrictEqual({ kind: 'skipped' })
  })

  it('drops a selection the question does not offer', () => {
    expect(kimiAnswer(single, ['z'], '')).toStrictEqual({ kind: 'skipped' })
    expect(kimiAnswer(single, ['z', 'b'], '')).toStrictEqual({ kind: 'single', option_id: 'b' })
    expect(kimiAnswer(multi, ['z', 'b'], '')).toStrictEqual({ kind: 'multi', option_ids: ['b'] })
    expect(kimiAnswer(multi, ['z'], 'typed')).toStrictEqual({ kind: 'other', text: 'typed' })
  })

  // The control keeps a choice and typed text exclusive for a single-choice question.
  // When both still arrive, the choice is the answer.
  it('answers a single-choice question with its choice when text arrives beside it', () => {
    expect(kimiAnswer(single, ['a'], 'typed')).toStrictEqual({ kind: 'single', option_id: 'a' })
  })

  it('matches an option that states no value by its label', () => {
    const labelled: ControlQuestion = { id: 'q_0', question: 'Q', options: [{ label: 'A' }] }
    expect(kimiAnswer(labelled, ['A'], '')).toStrictEqual({ kind: 'single', option_id: 'A' })
  })

  it('skips a question with no options and no text', () => {
    expect(kimiAnswer({ id: 'q_0', question: 'Q', options: [] }, [], '')).toStrictEqual({ kind: 'skipped' })
    expect(kimiAnswer({ id: 'q_0', question: 'Q', options: [], multiSelect: true }, ['x'], '')).toStrictEqual({ kind: 'skipped' })
  })
})

describe('buildKimiAnswers', () => {
  it('answers every question by its id in the neutral envelope', () => {
    const questions = kimiQuestionsFromPayload(REQUEST)
    const state = createControlAnswerState({ selections: { 0: ['opt_0_1'], 1: ['opt_1_0', 'opt_1_1'] }, customTexts: { 1: 'XL' } })
    expect(buildKimiAnswers('question_1', questions, state)).toStrictEqual({
      type: 'control_response',
      response: {
        subtype: 'success',
        request_id: 'question_1',
        response: {
          behavior: 'allow',
          answers: {
            q_0: { kind: 'single', option_id: 'opt_0_1' },
            q_1: { kind: 'multi_with_other', option_ids: ['opt_1_0', 'opt_1_1'], other_text: 'XL' },
            q_4: { kind: 'skipped' },
          },
        },
      },
    })
  })

  // The worker checks each answer against the stored request by id, so a question
  // with no id has no answer it can carry.
  it('answers no question that states no id', () => {
    const questions: ControlQuestion[] = [{ question: 'No id', options: [{ value: 'a', label: 'A' }] }, { id: 'q_1', question: 'Q', options: [] }]
    const state = createControlAnswerState({ selections: { 0: ['a'] }, customTexts: { 1: 'Mine' } })
    expect(buildKimiAnswers('question_1', questions, state)).toStrictEqual({
      type: 'control_response',
      response: { subtype: 'success', request_id: 'question_1', response: { behavior: 'allow', answers: { q_1: { kind: 'other', text: 'Mine' } } } },
    })
  })

  it('answers a request with no questions with an empty answer map', () => {
    expect(buildKimiAnswers('question_1', [], createControlAnswerState())).toStrictEqual({
      type: 'control_response',
      response: { subtype: 'success', request_id: 'question_1', response: { behavior: 'allow', answers: {} } },
    })
  })
})
