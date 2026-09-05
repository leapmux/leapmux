import type { ControlRequest } from '~/stores/control.store'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { buildAskAnswers, controlQuestion, trySubmitAskUserQuestion } from './AskUserQuestionControl'
import { createControlAnswerState } from './types'
import '../providers'

describe('trySubmitAskUserQuestion', () => {
  it('saves the current page draft and navigates to the next unanswered page', () => {
    const state = createControlAnswerState({
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
    const state = createControlAnswerState({
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
    const state = createControlAnswerState({
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

describe('controlQuestion', () => {
  const question = (): ControlRequest => ({
    requestId: 'ask-1',
    agentId: 'a1',
    payload: {
      request: {
        tool_name: 'AskUserQuestion',
        input: { questions: [{ question: 'Which database?', options: [{ label: 'Postgres' }] }] },
      },
    },
  })

  it('returns the capability and the questions for a question payload', () => {
    const found = controlQuestion(question(), AgentProvider.CLAUDE_CODE)
    expect(found?.questions).toHaveLength(1)
    expect(found?.questions[0].question).toBe('Which database?')
    expect(found?.capability).toBeDefined()
  })

  it('returns nothing for a control request that is not a question', () => {
    const plan: ControlRequest = {
      requestId: 'plan-1',
      agentId: 'a1',
      payload: { request: { tool_name: 'ExitPlanMode', input: {} } },
    }
    expect(controlQuestion(plan, AgentProvider.CLAUDE_CODE)).toBeUndefined()
  })

  // The banner runs this inside a memo that a store removal can re-run, so an
  // absent request has to be an answer rather than a dereference.
  it('returns nothing for an absent request instead of throwing', () => {
    expect(() => controlQuestion(null, AgentProvider.CLAUDE_CODE)).not.toThrow()
    expect(controlQuestion(null, AgentProvider.CLAUDE_CODE)).toBeUndefined()
    expect(controlQuestion(undefined, AgentProvider.CLAUDE_CODE)).toBeUndefined()
  })

  // An UNSPECIFIED or unregistered provider means backend/frontend version skew.
  // Classifying through some other provider's parser would answer the wrong shape.
  it('returns nothing when the provider has no plugin registered', () => {
    expect(controlQuestion(question(), AgentProvider.UNSPECIFIED)).toBeUndefined()
    expect(controlQuestion(question(), undefined)).toBeUndefined()
  })
})
