import { describe, expect, it } from 'vitest'
import { piQuestionsFromPayload, piSelectOptions } from './askUserQuestion'

describe('piSelectOptions', () => {
  it('returns labelled options for a string array', () => {
    expect(piSelectOptions({ options: ['Allow', 'Deny'] }))
      .toEqual([{ label: 'Allow' }, { label: 'Deny' }])
  })

  it('returns an empty array when options is missing', () => {
    expect(piSelectOptions({})).toEqual([])
  })

  it('returns an empty array when options is not an array', () => {
    expect(piSelectOptions({ options: 'not an array' })).toEqual([])
  })

  it('drops non-string entries defensively', () => {
    expect(piSelectOptions({ options: ['ok', 42, null, { label: 'x' }, 'ok2'] }))
      .toEqual([{ label: 'ok' }, { label: 'ok2' }])
  })
})

describe('piQuestionsFromPayload', () => {
  it('builds a select-method question from id/title/options', () => {
    expect(piQuestionsFromPayload({
      method: 'select',
      id: 'q1',
      title: 'Pick one',
      options: ['A', 'B'],
    })).toEqual([{
      id: 'q1',
      question: 'Pick one',
      options: [{ label: 'A' }, { label: 'B' }],
    }])
  })

  it('falls back to a default title when select payload omits one', () => {
    expect(piQuestionsFromPayload({ method: 'select', options: ['Yes'] }))
      .toEqual([{ id: '', question: 'Choose an option', options: [{ label: 'Yes' }] }])
  })

  it('builds an input-method question with no options and a default prompt', () => {
    expect(piQuestionsFromPayload({ method: 'input', id: 'qi' }))
      .toEqual([{ id: 'qi', question: 'Enter a value', options: [] }])
  })

  it('preserves the title for input-method requests when provided', () => {
    expect(piQuestionsFromPayload({ method: 'input', id: 'qi', title: 'Custom prompt' }))
      .toEqual([{ id: 'qi', question: 'Custom prompt', options: [] }])
  })

  it('routes unknown methods through the input-shape default (single fallback)', () => {
    // Pi may add new dialog methods (editor, ...) — they should still
    // surface as a question rather than crash. The fallback shape keeps
    // a UX-safe default until the helper is taught the new method.
    expect(piQuestionsFromPayload({ method: 'editor', id: 'qe', title: 'Edit' }))
      .toEqual([{ id: 'qe', question: 'Edit', options: [] }])
  })
})
