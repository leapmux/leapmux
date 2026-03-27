import type { AskQuestionState, Question } from './types'
import type { ControlRequest } from '~/stores/control.store'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { buildAskAnswers, trySubmitAskUserQuestion } from './AskUserQuestionControl'

function makeAskState(overrides: {
  selections?: Record<number, string[]>
  customTexts?: Record<number, string>
  currentPage?: number
} = {}): AskQuestionState {
  const [selections, setSelections] = createSignal<Record<number, string[]>>(overrides.selections ?? {})
  const [customTexts, setCustomTexts] = createSignal<Record<number, string>>(overrides.customTexts ?? {})
  const [currentPage, setCurrentPage] = createSignal(overrides.currentPage ?? 0)
  return { selections, setSelections, customTexts, setCustomTexts, currentPage, setCurrentPage }
}

function makeRequest(questions: Question[]): ControlRequest {
  return {
    requestId: 'req-1',
    agentId: 'agent-1',
    payload: {
      request: {
        tool_name: 'AskUserQuestion',
        input: { questions },
      },
    },
  }
}

describe('trySubmitAskUserQuestion', () => {
  it('saves the current page draft and navigates to the next unanswered page', () => {
    const state = makeAskState({
      currentPage: 0,
    })
    const editorContentRef = { set: vi.fn(), get: vi.fn() }
    const submitted = trySubmitAskUserQuestion(
      state,
      makeRequest([
        { header: 'Task', question: 'Pick a task', options: [{ label: 'Build' }] },
        { header: 'Env', question: 'Pick an env', options: [{ label: 'Dev' }] },
      ]),
      'typed first answer',
      vi.fn(),
      editorContentRef,
    )

    expect(submitted).toBe(false)
    expect(state.customTexts()[0]).toBe('typed first answer')
    expect(state.currentPage()).toBe(1)
    expect(editorContentRef.set).toHaveBeenCalledWith('')
  })
})

describe('buildAskAnswers', () => {
  it('prefers selected options over custom text for the same question', () => {
    const state = makeAskState({
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
              Task: 'Build',
            },
          },
        },
      },
    })
  })
})
