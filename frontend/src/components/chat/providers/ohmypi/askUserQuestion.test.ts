import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { createControlAnswerState } from '../../controls/types'
import { isOhMyPiAskRequest, isOhMyPiDialogQuestion, ohMyPiAskAnswers, ohMyPiDialogAnswer, ohMyPiQuestionsFromPayload } from './askUserQuestion'

/** The question bridge's request for a two-question `ask` call. */
const askRequest = {
  type: 'leapmux_ask',
  id: '158b2ba4d71bfb8f',
  toolCallId: 'call_3',
  questions: [
    { id: 'name', question: 'Project name?', options: [{ label: 'alpha' }, { label: 'beta' }] },
    { id: 'langs', question: 'Which languages?', header: 'Languages', multi: true, options: [{ label: 'Go' }, { label: 'Rust' }], recommended: 1 },
  ],
}

/** omp 18.2.11's own tool approval dialog (probe s2). */
const approval = { type: 'extension_ui_request', id: '158b2ba5001bfb93', method: 'select', title: 'Allow tool: bash\nCommand: echo approved-run', options: ['Approve', 'Deny'] }

const dialog = (method: string, extra: Record<string, unknown> = {}) => ({ type: 'extension_ui_request', id: 'd1', method, ...extra })

describe('isOhMyPiAskRequest', () => {
  it('holds for the bridge\'s request alone', () => {
    expect(isOhMyPiAskRequest(askRequest)).toBe(true)
    expect(isOhMyPiAskRequest(approval)).toBe(false)
    expect(isOhMyPiAskRequest({ type: 'leapmux_ask_answer' })).toBe(false)
  })
})

describe('isOhMyPiDialogQuestion', () => {
  // A confirm, an input and an editor draw as a dialog of their own
  // (`ohMyPiExtractControl`), which states the draft, the hint and the deadline.
  it('holds for a select an extension raises, and not for the approval or another dialog', () => {
    expect(isOhMyPiDialogQuestion(dialog('select'))).toBe(true)
    for (const method of ['confirm', 'input', 'editor'])
      expect(isOhMyPiDialogQuestion(dialog(method)), method).toBe(false)
    expect(isOhMyPiDialogQuestion(approval)).toBe(false)
    expect(isOhMyPiDialogQuestion(dialog('notify'))).toBe(false)
    expect(isOhMyPiDialogQuestion({ type: 'response', method: 'select' })).toBe(false)
  })

  it('reads a select titled like an approval but without its answers as a question', () => {
    expect(isOhMyPiDialogQuestion(dialog('select', { title: 'Allow tool: x', options: ['Yes', 'No'] }))).toBe(true)
  })
})

describe('ohMyPiQuestionsFromPayload', () => {
  it('reads every question of the bridge\'s request', () => {
    expect(ohMyPiQuestionsFromPayload(askRequest)).toEqual([
      { id: 'name', question: 'Project name?', options: [{ label: 'alpha' }, { label: 'beta' }] },
      { id: 'langs', question: 'Which languages?', header: 'Languages', options: [{ label: 'Go' }, { label: 'Rust', description: 'Recommended' }], multiSelect: true },
    ])
  })

  it('reads a select with the descriptions omp sends beside its options', () => {
    // omp 18.2.11's own dialog (probe s2).
    expect(ohMyPiQuestionsFromPayload(dialog('select', { title: 'Which database?', options: ['SQLite (Recommended)', 'PostgreSQL'], optionDetails: [{ description: 'Single file' }, {}] }))).toEqual([
      { question: 'Which database?', options: [{ label: 'SQLite (Recommended)', description: 'Single file' }, { label: 'PostgreSQL' }] },
    ])
  })

  it('gives a select with no title a question of its own', () => {
    expect(ohMyPiQuestionsFromPayload(dialog('select', { options: ['a'] }))).toEqual([{ question: 'Choose an option', options: [{ label: 'a' }] }])
  })

  it('reads a select whose details are absent, shorter than its options, or not records', () => {
    expect(ohMyPiQuestionsFromPayload(dialog('select', { title: 'Pick', options: ['a', 'b'], optionDetails: 'none' }))).toEqual([
      { question: 'Pick', options: [{ label: 'a' }, { label: 'b' }] },
    ])
    expect(ohMyPiQuestionsFromPayload(dialog('select', { title: 'Pick', options: ['a', 'b', 'c'], optionDetails: ['x', { description: '' }] }))).toEqual([
      { question: 'Pick', options: [{ label: 'a' }, { label: 'b' }, { label: 'c' }] },
    ])
  })

  it('reads a select with no options, or options that are not words, as a question with no options', () => {
    expect(ohMyPiQuestionsFromPayload(dialog('select', { title: 'Pick' }))).toEqual([{ question: 'Pick', options: [] }])
    expect(ohMyPiQuestionsFromPayload(dialog('select', { title: 'Pick', options: [1, { label: 'x' }, 'kept'] }))).toEqual([{ question: 'Pick', options: [{ label: 'kept' }] }])
  })

  it('asks no question for a request that is neither the bridge\'s nor a select', () => {
    for (const method of ['confirm', 'input', 'editor', 'notify'])
      expect(ohMyPiQuestionsFromPayload(dialog(method, { title: 'Proceed?' })), method).toEqual([])
    expect(ohMyPiQuestionsFromPayload({})).toEqual([])
  })

  it('reads a bridge request with no questions as none', () => {
    expect(ohMyPiQuestionsFromPayload({ type: 'leapmux_ask', id: 'q' })).toEqual([])
    expect(ohMyPiQuestionsFromPayload({ type: 'leapmux_ask', id: 'q', questions: [] })).toEqual([])
  })
})

describe('ohMyPiAskAnswers', () => {
  it('reads one answer per question from the form\'s state', () => {
    createRoot((dispose) => {
      const state = createControlAnswerState({ selections: { 1: ['Go', 'Rust'] }, customTexts: { 0: 'gamma' } })
      expect(ohMyPiAskAnswers(ohMyPiQuestionsFromPayload(askRequest), state)).toEqual([
        { id: 'name', selected: [], custom: 'gamma' },
        { id: 'langs', selected: ['Go', 'Rust'], custom: '' },
      ])
      dispose()
    })
  })

  it('states an empty id for a question with none, and reads no answer for no question', () => {
    createRoot((dispose) => {
      const state = createControlAnswerState({ selections: { 0: ['a'] } })
      expect(ohMyPiAskAnswers([{ question: 'Pick', options: [{ label: 'a' }] }], state)).toEqual([{ id: '', selected: ['a'], custom: '' }])
      expect(ohMyPiAskAnswers([], state)).toEqual([])
      dispose()
    })
  })
})

describe('ohMyPiDialogAnswer', () => {
  it('reads the chosen option, else the typed text', () => {
    createRoot((dispose) => {
      expect(ohMyPiDialogAnswer(createControlAnswerState({ selections: { 0: ['PostgreSQL'] } }))).toBe('PostgreSQL')
      expect(ohMyPiDialogAnswer(createControlAnswerState({ customTexts: { 0: 'main' } }))).toBe('main')
      expect(ohMyPiDialogAnswer(createControlAnswerState())).toBe('')
      dispose()
    })
  })

  it('prefers the chosen option to the typed text, and reads an empty choice as no choice', () => {
    createRoot((dispose) => {
      expect(ohMyPiDialogAnswer(createControlAnswerState({ selections: { 0: ['PostgreSQL'] }, customTexts: { 0: 'main' } }))).toBe('PostgreSQL')
      expect(ohMyPiDialogAnswer(createControlAnswerState({ selections: { 0: [] }, customTexts: { 0: 'main' } }))).toBe('main')
      dispose()
    })
  })
})
