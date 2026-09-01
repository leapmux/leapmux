import { describe, expect, it } from 'vitest'
import { ZCODE_TOOL } from '~/generated/contracts/zcode-protocol'
import { zcodePlanText, zcodeQuestionsFromPayload } from './askUserQuestion'

/** A stored control-request payload, as the worker persists an interaction request. */
function payload(input: Record<string, unknown>, toolName: string = ZCODE_TOOL.AskUserQuestion): Record<string, unknown> {
  return { request: { tool_name: toolName, input } }
}

describe('zcodeQuestionsFromPayload', () => {
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

describe('zcodePlanText', () => {
  // `plan` is where the worker puts the plan, and it wins over everything else. A plan
  // approval carries no question of its own, so the worker synthesizes one whose text is
  // fixed boilerplate — reading the question first rendered THAT as the plan, and the
  // real plan never appeared.
  it('reads the plan from the plan field, over a synthesized boilerplate question', () => {
    expect(zcodePlanText(payload({
      plan: '## Plan\n1. Do the thing',
      questions: [{ question: 'Review this implementation plan.' }],
      prompt: 'Review this implementation plan.',
    }, ZCODE_TOOL.ExitPlanMode))).toBe('## Plan\n1. Do the thing')
  })

  // The question text stays as a fallback, for a build that states the plan there.
  it('reads the plan from the first question text', () => {
    expect(zcodePlanText(payload({
      questions: [{ question: '## Plan\n1. Do the thing' }],
    }, ZCODE_TOOL.ExitPlanMode))).toBe('## Plan\n1. Do the thing')
  })

  it('skips a leading question that carries no text', () => {
    expect(zcodePlanText(payload({
      questions: [{ header: 'only a header' }, { question: 'the plan' }],
    }, ZCODE_TOOL.ExitPlanMode))).toBe('the plan')
  })

  it('falls back to the request prompt when no question carries text', () => {
    expect(zcodePlanText(payload({
      prompt: 'approve the plan?',
      questions: [{ header: 'no text here' }],
    }, ZCODE_TOOL.ExitPlanMode))).toBe('approve the plan?')
  })

  it('falls back to the prompt when the questions field is absent or malformed', () => {
    expect(zcodePlanText(payload({ prompt: 'approve?' }, ZCODE_TOOL.ExitPlanMode))).toBe('approve?')
    expect(zcodePlanText(payload({ prompt: 'approve?', questions: {} }, ZCODE_TOOL.ExitPlanMode)))
      .toBe('approve?')
  })

  // The content component falls back to its own sentence for an empty plan, so an
  // empty string here is a valid answer and not a reason to throw.
  it('reports an empty string when the request states neither', () => {
    expect(zcodePlanText(payload({}, ZCODE_TOOL.ExitPlanMode))).toBe('')
    expect(zcodePlanText({})).toBe('')
  })
})
