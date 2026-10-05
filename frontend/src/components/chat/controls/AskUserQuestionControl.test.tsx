import type { Mock } from 'vitest'
import type { ControlAnswerState } from './types'
import type { ControlRequest } from '~/stores/control.store'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '~/lib/jsonPick'
import { ControlRequestActions } from '~/test-support/controlRequestBanner'
import { buildAskAnswers, controlQuestion, submitBlockedReason, trySubmitAskUserQuestion } from './AskUserQuestionControl'
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
    expect(found?.questions[0]?.question).toBe('Which database?')
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

describe('submitBlockedReason', () => {
  const options = [{ label: 'Build' }]
  const answered = () => true
  const unanswered = () => false

  it('states nothing when every question is answered', () => {
    expect(submitBlockedReason([{ question: 'Pick a task', options }], answered)).toBe('')
  })

  // The census that measured this control read a disabled submit beside preset
  // options as "these options are the only answer", and reported the typed
  // answer as unreachable. Both routes are named here for that reason.
  it('names both routes when the waiting question offers options', () => {
    expect(submitBlockedReason([{ question: 'Pick a task', options }], unanswered))
      .toBe('Choose an option, or type a custom answer below.')
  })

  it('names only the typed route when the waiting question offers no option', () => {
    expect(submitBlockedReason([{ question: 'Name it', options: [] }], unanswered))
      .toBe('Type a custom answer below.')
  })

  it('adds the every-question sentence when the control carries more than one', () => {
    expect(submitBlockedReason([
      { question: 'Pick a task', options },
      { question: 'Pick an env', options },
    ], unanswered)).toBe('Every question needs an answer. Choose an option, or type a custom answer below.')
  })

  // The reason describes the question the submit WAITS for, which is the first
  // unanswered one -- not whichever page the reader happens to be reading.
  it('describes the first unanswered question, not the current page', () => {
    expect(submitBlockedReason([
      { question: 'Name it', options: [] },
      { question: 'Pick an env', options },
    ], index => index === 1)).toBe('Every question needs an answer. Type a custom answer below.')
  })

  it('states the empty case for a request that carries no question', () => {
    expect(submitBlockedReason([], unanswered)).toBe('This request carries no question to answer.')
  })
})

describe('AskUserQuestionActions', () => {
  const YOLO_ANSWER = 'Go with the recommended option.'

  const colorAndSize = (): ControlRequest => ({
    requestId: 'ask-yolo',
    agentId: 'a1',
    payload: {
      request: {
        tool_name: 'AskUserQuestion',
        input: {
          questions: [
            { question: 'Pick a color', header: 'Color', options: [{ label: 'Red' }, { label: 'Blue' }] },
            { question: 'Pick a size', header: 'Size', options: [{ label: 'Small' }, { label: 'Large' }] },
          ],
        },
      },
    },
  })

  /** Render the Claude question actions with an editor that holds `editorText` on the current page. */
  function renderActions(state: ControlAnswerState, editorText: string): Mock {
    const respond = vi.fn().mockResolvedValue(undefined)
    const editor = { get: () => editorText, set: vi.fn() }
    render(() => (
      <ControlRequestActions
        request={colorAndSize()}
        answerState={state}
        agentProvider={AgentProvider.CLAUDE_CODE}
        onRespond={respond}
        hasEditorContent={editorText !== ''}
        onTriggerSend={() => {}}
        editorContentRef={() => editor}
      />
    ))
    return respond
  }

  /** Decode the answers map of the one control response that `respond` received. */
  function sentAnswers(respond: Mock): unknown {
    const bytes: unknown = respond.mock.calls[0]?.[0]
    // A realm check (`instanceof Uint8Array`) is unreliable here: the encoder
    // and the test environment can hold different Uint8Array constructors.
    if (!ArrayBuffer.isView(bytes))
      throw new Error(`The question actions sent no response bytes: ${String(bytes)}`)
    const envelope: unknown = JSON.parse(new TextDecoder().decode(bytes))
    const outer = isObject(envelope) ? envelope.response : undefined
    const inner = isObject(outer) ? outer.response : undefined
    const input = isObject(inner) ? inner.updatedInput : undefined
    return isObject(input) ? input.answers : undefined
  }

  // An option click on the first page advances to the second, so YOLO runs
  // with the unanswered page CURRENT and an empty editor. The submit saves the
  // editor into the current page, and that save must not erase the answer that
  // YOLO filled in.
  it('fills the current unanswered page when the editor is empty', async () => {
    const state = createControlAnswerState({ selections: { 0: ['Red'] }, currentPage: 1 })
    const respond = renderActions(state, '')

    fireEvent.click(screen.getByTestId('control-yolo-btn'))

    await vi.waitFor(() => expect(respond).toHaveBeenCalledOnce())
    expect(sentAnswers(respond)).toEqual({ 'Pick a color': 'Red', 'Pick a size': YOLO_ANSWER })
  })

  // The first page stays unanswered, so YOLO stays enabled. Unsaved editor text
  // answers the current page, and YOLO must keep it.
  it('keeps the unsaved editor text of the current page instead of the recommended option', async () => {
    const state = createControlAnswerState({ currentPage: 1 })
    const respond = renderActions(state, 'Medium, please')

    fireEvent.click(screen.getByTestId('control-yolo-btn'))

    await vi.waitFor(() => expect(respond).toHaveBeenCalledOnce())
    expect(sentAnswers(respond)).toEqual({ 'Pick a color': YOLO_ANSWER, 'Pick a size': 'Medium, please' })
  })

  it('stays disabled when unsaved editor text answers the only open page', () => {
    const state = createControlAnswerState({ selections: { 0: ['Red'] }, currentPage: 1 })
    renderActions(state, 'Medium, please')

    expect(screen.getByTestId('control-yolo-btn')).toBeDisabled()
  })

  it('fills an unanswered page that is not current', async () => {
    const state = createControlAnswerState({ selections: { 0: ['Red'] }, currentPage: 0 })
    const respond = renderActions(state, '')

    fireEvent.click(screen.getByTestId('control-yolo-btn'))

    await vi.waitFor(() => expect(respond).toHaveBeenCalledOnce())
    expect(sentAnswers(respond)).toEqual({ 'Pick a color': 'Red', 'Pick a size': YOLO_ANSWER })
  })

  it('fills every page when no page has an answer', async () => {
    const state = createControlAnswerState({ currentPage: 0 })
    const respond = renderActions(state, '')

    fireEvent.click(screen.getByTestId('control-yolo-btn'))

    await vi.waitFor(() => expect(respond).toHaveBeenCalledOnce())
    expect(sentAnswers(respond)).toEqual({ 'Pick a color': YOLO_ANSWER, 'Pick a size': YOLO_ANSWER })
  })
})
