import { describe, expect, it } from 'vitest'
import { ZCODE_TOOL } from '~/generated/contracts/zcode-protocol'
import { zcodeQuestionsFromPayload } from './askUserQuestion'

/** A stored control-request payload, as the worker persists an interaction request. */
function payload(input: Record<string, unknown>, toolName: string = ZCODE_TOOL.AskUserQuestion): Record<string, unknown> {
  return { request: { tool_name: toolName, input } }
}

describe('zcodeQuestionsFromPayload', () => {
  it('recovers option descriptions from the preserved native request', () => {
    const nativeQuestion = {
      question: 'Pick a color.',
      header: 'Color',
      multiSelect: false,
      options: [{ label: 'Blue', value: 'Blue', description: 'Choose the color blue.' }],
    }
    const original = {
      ...payload({ questions: [{ ...nativeQuestion, options: [{ label: 'Blue', value: 'Blue' }] }] }),
      params: { questions: [nativeQuestion], schema: { toolName: 'AskUserQuestion' } },
    }
    const before = JSON.stringify(original)
    expect(zcodeQuestionsFromPayload(original)[0].options).toEqual([{ label: 'Blue', description: 'Choose the color blue.' }])
    expect(JSON.stringify(original)).toBe(before)
  })

  it.each(['schema', 'input'])('reads native questions from %s', (field) => {
    expect(zcodeQuestionsFromPayload({ params: { [field]: { questions: [{ question: 'Native question', options: [{ label: 'A', description: 'Native description' }] }] } } })).toEqual([
      { question: 'Native question', options: [{ label: 'A', description: 'Native description' }] },
    ])
  })

  it('keeps an explicitly empty native question list empty', () => {
    expect(zcodeQuestionsFromPayload({
      ...payload({ questions: [{ question: 'Stale question' }] }),
      params: { questions: [] },
    })).toEqual([])
  })

  it('uses native top-level questions when the schema list is empty', () => {
    expect(zcodeQuestionsFromPayload({ params: { schema: { questions: [] }, questions: [{ question: 'Native question' }] } })).toEqual([
      { question: 'Native question', options: [] },
    ])
  })

  it('reads a question and its options through', () => {
    expect(zcodeQuestionsFromPayload(payload({
      questions: [{
        question: 'Which database?',
        options: [{ value: 'Postgres' }, { value: 'MySQL', description: 'the other one' }],
      }],
    }))).toEqual([{
      question: 'Which database?',
      options: [{ label: 'Postgres' }, { label: 'MySQL', description: 'the other one' }],
    }])
  })

  // ZCode's wire form sets an option's `value` to its own LABEL, and the shared
  // control keys the answer by the label it shows -- so either field standing in for
  // the other keeps the option answerable.
  it('accepts either spelling of an option label', () => {
    expect(zcodeQuestionsFromPayload(payload({
      questions: [{ question: 'Q', options: [{ label: 'from label' }, { value: 'from value' }] }],
    }))[0].options).toEqual([{ label: 'from label' }, { label: 'from value' }])
  })

  it('prefers label over value when the app-server sends both', () => {
    expect(zcodeQuestionsFromPayload(payload({
      questions: [{ question: 'Q', options: [{ label: 'shown', value: 'ignored' }] }],
    }))[0].options).toEqual([{ label: 'shown' }])
  })

  // An option with neither field has nothing to send, so it is dropped rather than
  // rendered as a blank button the app-server would discard the answer for.
  it('drops an option with no label and no value', () => {
    expect(zcodeQuestionsFromPayload(payload({
      questions: [{ question: 'Q', options: [{ description: 'orphan' }, {}, 'not an object', { value: 'keep' }] }],
    }))[0].options).toEqual([{ label: 'keep' }])
  })

  it('omits an empty description rather than sending a blank one', () => {
    expect(zcodeQuestionsFromPayload(payload({
      questions: [{ question: 'Q', options: [{ value: 'A', description: '' }] }],
    }))[0].options).toEqual([{ label: 'A' }])
  })

  it('carries the header and the multiSelect flag when the request states them', () => {
    expect(zcodeQuestionsFromPayload(payload({
      questions: [{ question: 'Pick any', header: 'Databases', multiSelect: true, options: [{ value: 'A' }] }],
    }))).toEqual([{
      question: 'Pick any',
      header: 'Databases',
      multiSelect: true,
      options: [{ label: 'A' }],
    }])
  })

  it('omits multiSelect unless it is explicitly true', () => {
    for (const multiSelect of [false, 'true', undefined]) {
      const [question] = zcodeQuestionsFromPayload(payload({
        questions: [{ question: 'Q', multiSelect, options: [{ value: 'A' }] }],
      }))
      expect(question.multiSelect).toBeUndefined()
    }
  })

  // The answer is keyed by the question TEXT, so a header-only question is answerable
  // through its header and must not be dropped.
  it('uses the header as the question text when only the header is present', () => {
    expect(zcodeQuestionsFromPayload(payload({
      questions: [{ header: 'Databases', options: [{ value: 'A' }] }],
    }))).toEqual([{ question: 'Databases', header: 'Databases', options: [{ label: 'A' }] }])
  })

  it('drops a question with neither text nor header, which could never be answered', () => {
    expect(zcodeQuestionsFromPayload(payload({
      questions: [{ options: [{ value: 'A' }] }, 'not an object', null, { question: 'kept' }],
    }))).toEqual([{ question: 'kept', options: [] }])
  })

  it('reads every question of a multi-question prompt, in order', () => {
    expect(zcodeQuestionsFromPayload(payload({
      questions: [{ question: 'First' }, { question: 'Second' }],
    })).map(q => q.question)).toEqual(['First', 'Second'])
  })

  it('reports an empty question with no options rather than throwing', () => {
    expect(zcodeQuestionsFromPayload(payload({ questions: [{ question: 'Q' }] })))
      .toEqual([{ question: 'Q', options: [] }])
  })

  // A plan approval reaches the plan surface instead, which needs no question list.
  it('returns an empty list for a request that declares no questions', () => {
    expect(zcodeQuestionsFromPayload(payload({}))).toEqual([])
    expect(zcodeQuestionsFromPayload(payload({ questions: 'not an array' }))).toEqual([])
    expect(zcodeQuestionsFromPayload({})).toEqual([])
  })
})
