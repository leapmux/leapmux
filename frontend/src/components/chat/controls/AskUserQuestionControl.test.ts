import { describe, expect, it, vi } from 'vitest'
import { createAskQuestionState } from '~/test-support/askQuestionState'
import { buildAskAnswers, trySubmitAskUserQuestion } from './AskUserQuestionControl'

describe('trySubmitAskUserQuestion', () => {
  it('saves the current page draft and navigates to the next unanswered page', () => {
    const state = createAskQuestionState({
      currentPage: 0,
    })
    const editorContentRef = { set: vi.fn(), get: vi.fn() }
    const submitted = trySubmitAskUserQuestion(
      state,
      [
        { header: 'Task', question: 'Pick a task', options: [{ label: 'Build' }] },
        { header: 'Env', question: 'Pick an env', options: [{ label: 'Dev' }] },
      ],
      'typed first answer',
      vi.fn(),
      editorContentRef,
    )

    expect(submitted).toBe(false)
    expect(state.customTexts()[0]).toBe('typed first answer')
    expect(state.currentPage()).toBe(1)
    expect(editorContentRef.set).toHaveBeenCalledWith('')
  })

  it('can preserve selected options when editor text is provider-specific notes', () => {
    const state = createAskQuestionState({
      selections: { 0: ['Build'] },
    })
    const onSubmit = vi.fn()
    const submitted = trySubmitAskUserQuestion(
      state,
      [
        { header: 'Task', question: 'Pick a task', options: [{ label: 'Build' }] },
      ],
      'note for selected option',
      onSubmit,
      undefined,
      true,
    )

    expect(submitted).toBe(true)
    expect(onSubmit).toHaveBeenCalledOnce()
    expect(state.customTexts()[0]).toBe('note for selected option')
    expect(state.selections()[0]).toEqual(['Build'])
  })
})

describe('buildAskAnswers', () => {
  it('keys answers by question text as expected by Claude Code', () => {
    const state = createAskQuestionState({
      selections: { 0: ['Build'] },
      customTexts: { 0: 'typed answer' },
    })
    const result = buildAskAnswers(
      state,
      [{ header: 'Task', question: 'Pick a task', options: [{ label: 'Build' }] }],
      { questions: [] },
      'req-1',
    )

    expect(result).toMatchObject({
      response: {
        request_id: 'req-1',
        response: {
          updatedInput: {
            answers: {
              'Pick a task': 'Build',
            },
          },
        },
      },
    })
  })
})
