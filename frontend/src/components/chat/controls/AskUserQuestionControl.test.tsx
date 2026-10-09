import type { Mock } from 'vitest'
import type { ControlAnswerState } from './types'
import type { ControlRequest } from '~/stores/control.store'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '~/lib/jsonPick'
import { ControlRequestActions } from '~/test-support/controlRequestBanner'
import { AskUserQuestionActions, AskUserQuestionContent, buildAskAnswers, controlQuestion, submitBlockedReason, trySubmitAskUserQuestion } from './AskUserQuestionControl'
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

  it('states the explicit selection count for the unanswered question', () => {
    const questions = [{ question: 'Choose the tools', options: [{ label: 'One' }, { label: 'Two' }, { label: 'Three' }], multiSelect: true, minimumSelections: 2, maximumSelections: 3 }]
    expect(submitBlockedReason(questions, unanswered)).toBe('Select 2 to 3 options, or type a custom answer below.')
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

describe('trySubmitAskUserQuestion selection limits', () => {
  const question = (minimumSelections?: number, maximumSelections?: number) => ({
    question: 'Choose the tools',
    options: [{ label: 'One' }, { label: 'Two' }, { label: 'Three' }],
    multiSelect: true,
    ...(minimumSelections === undefined ? {} : { minimumSelections }),
    ...(maximumSelections === undefined ? {} : { maximumSelections }),
  })

  it('refuses an empty question list through the production submit helper', () => {
    const state = createControlAnswerState()
    const submit = vi.fn()
    expect(trySubmitAskUserQuestion(state, [], 'Native answer', submit)).toBe(false)
    expect(submit).not.toHaveBeenCalled()
  })

  it.each([
    { label: 'below the minimum', selected: ['One'], minimum: 2, maximum: 3, allowed: false },
    { label: 'at the minimum', selected: ['One', 'Two'], minimum: 2, maximum: 3, allowed: true },
    { label: 'at the maximum', selected: ['One', 'Two', 'Three'], minimum: 2, maximum: 3, allowed: true },
    { label: 'above the maximum', selected: ['One', 'Two', 'Three'], minimum: 1, maximum: 2, allowed: false },
    { label: 'explicit zero minimum', selected: ['One'], minimum: 0, maximum: 1, allowed: true },
    { label: 'explicit zero maximum', selected: ['One'], minimum: 0, maximum: 0, allowed: false },
    { label: 'absent limits', selected: ['One'], minimum: undefined, maximum: undefined, allowed: true },
  ])('checks $label before the production submit helper sends an answer', ({ selected, minimum, maximum, allowed }) => {
    const state = createControlAnswerState({ selections: { 0: selected } })
    const submit = vi.fn()
    expect(trySubmitAskUserQuestion(state, [question(minimum, maximum)], '', submit, undefined, true)).toBe(allowed)
    expect(submit).toHaveBeenCalledTimes(allowed ? 1 : 0)
    expect(state.selections()[0]).toEqual(selected)
  })

  it.each([
    { label: 'typed answer', text: '  Native answer 界\n', allowEmpty: false, allowed: true },
    { label: 'empty answer', text: '', allowEmpty: false, allowed: false },
    { label: 'whitespace answer', text: ' \t\n', allowEmpty: false, allowed: false },
    { label: 'explicit empty allowance', text: '', allowEmpty: true, allowed: true },
  ])('keeps $label separate from option counts', ({ text, allowEmpty, allowed }) => {
    const state = createControlAnswerState()
    const submit = vi.fn()
    expect(trySubmitAskUserQuestion(state, [{ ...question(2, 3), allowEmpty }], text, submit)).toBe(allowed)
    expect(submit).toHaveBeenCalledTimes(allowed ? 1 : 0)
    expect(state.customTexts()[0]).toBe(text)
    expect(state.selections()[0] ?? []).toEqual([])
  })

  it('keeps a selected note from bypassing the option minimum', () => {
    const state = createControlAnswerState({ selections: { 0: ['One'] } })
    const submit = vi.fn()
    expect(trySubmitAskUserQuestion(state, [question(2, 3)], 'Native note', submit, undefined, true)).toBe(false)
    expect(submit).not.toHaveBeenCalled()
    expect(state.selections()[0]).toEqual(['One'])
    expect(state.customTexts()[0]).toBe('Native note')
  })

  it('keeps an empty-answer allowance from bypassing selection limits', () => {
    const state = createControlAnswerState({ selections: { 0: ['One'] } })
    const submit = vi.fn()
    expect(trySubmitAskUserQuestion(state, [{ ...question(2, 3), allowEmpty: true }], '', submit, undefined, true)).toBe(false)
    expect(submit).not.toHaveBeenCalled()
    expect(state.selections()[0]).toEqual(['One'])
  })

  it('moves to a page whose selection does not fit its own limits', () => {
    const state = createControlAnswerState({ currentPage: 0, selections: { 0: ['One'], 1: ['One'] } })
    const submit = vi.fn()
    const editor = { get: vi.fn(), set: vi.fn() }
    expect(trySubmitAskUserQuestion(state, [question(1, 1), question(2, 3)], '', submit, editor, true)).toBe(false)
    expect(state.currentPage()).toBe(1)
    expect(editor.set).toHaveBeenCalledWith('')
    expect(state.selections()).toEqual({ 0: ['One'], 1: ['One'] })
    expect(submit).not.toHaveBeenCalled()
  })
})

describe('AskUserQuestionContent selection limits', () => {
  function renderLimitedQuestion(selected: string[] = [], hasEditorContent = false) {
    const state = createControlAnswerState({ selections: { 0: selected } })
    const questions = [{ question: 'Choose the tools', options: [{ label: 'One' }, { label: 'Two' }, { label: 'Three' }], multiSelect: true, minimumSelections: 2, maximumSelections: 2 }]
    const request: ControlRequest = { requestId: 'limited-question', agentId: 'agent', payload: {} }
    const submit = vi.fn().mockResolvedValue(undefined)
    render(() => (
      <>
        <AskUserQuestionContent request={request} answerState={state} questions={questions} agentProvider={AgentProvider.MUSE_CODE} />
        <AskUserQuestionActions request={request} answerState={state} questions={questions} agentProvider={AgentProvider.MUSE_CODE} onRespond={vi.fn()} hasEditorContent={hasEditorContent} onTriggerSend={vi.fn()} onSubmitAnswers={submit} onReject={vi.fn()} />
      </>
    ))
    return { state, submit }
  }

  it('shows the count instruction and permits a complete selection', async () => {
    const { state, submit } = renderLimitedQuestion()
    expect(screen.getByText('Select 2 options if you use options.')).toBeInTheDocument()
    expect(screen.getByTestId('control-submit-btn')).toBeDisabled()
    fireEvent.click(screen.getByRole('checkbox', { name: 'One' }))
    expect(screen.getByTestId('control-submit-btn')).toBeDisabled()
    fireEvent.click(screen.getByRole('checkbox', { name: 'Two' }))
    expect(screen.getByTestId('control-submit-btn')).not.toBeDisabled()
    fireEvent.click(screen.getByTestId('control-submit-btn'))
    await vi.waitFor(() => expect(submit).toHaveBeenCalledOnce())
    expect(state.selections()[0]).toEqual(['One', 'Two'])
  })

  it('disables only unchecked options at the maximum and permits removal', () => {
    const { state } = renderLimitedQuestion(['One', 'Two'])
    expect(screen.getByRole('checkbox', { name: 'Three' })).toBeDisabled()
    expect(screen.getByRole('checkbox', { name: 'One' })).not.toBeDisabled()
    fireEvent.click(screen.getByRole('checkbox', { name: 'One' }))
    expect(state.selections()[0]).toEqual(['Two'])
    expect(screen.getByRole('checkbox', { name: 'Three' })).not.toBeDisabled()
    expect(screen.getByTestId('control-submit-btn')).toBeDisabled()
  })

  it('keeps an excessive restored selection until the user corrects it', () => {
    const { state, submit } = renderLimitedQuestion(['One', 'Two', 'Three'])
    expect(screen.getByTestId('control-submit-btn')).toBeDisabled()
    expect(state.selections()[0]).toEqual(['One', 'Two', 'Three'])
    expect(submit).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('checkbox', { name: 'Three' }))
    expect(state.selections()[0]).toEqual(['One', 'Two'])
    expect(screen.getByTestId('control-submit-btn')).not.toBeDisabled()
  })

  it('does not treat unsaved note text as a complete partial selection', () => {
    renderLimitedQuestion(['One'], true)
    expect(screen.getByTestId('control-submit-btn')).toBeDisabled()
  })

  it('replaces a partial selection with the automatic typed answer before submission', async () => {
    const { state, submit } = renderLimitedQuestion(['One'])
    fireEvent.click(screen.getByTestId('control-yolo-btn'))
    await vi.waitFor(() => expect(submit).toHaveBeenCalledOnce())
    expect(state.selections()[0] ?? []).toEqual([])
    expect(state.customTexts()[0]?.trim()).not.toBe('')
  })
})
