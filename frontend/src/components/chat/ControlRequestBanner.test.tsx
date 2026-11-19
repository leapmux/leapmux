import type { ControlDecisionSender } from './controls/types'
import type { ControlQuestion } from './model/question'
import type { QuestionDecision } from '~/generated/proto/leapmux/v1/agent_control_pb'
import type { ControlRequest } from '~/stores/control.store'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { compactControl } from '~/components/common/CompactControl.css'
import { ALL_PROVIDERS } from '~/generated/contracts/providers'
import { DialogAction } from '~/generated/proto/leapmux/v1/agent_control_pb'
import { AgentProvider, ControlResponseState } from '~/generated/proto/leapmux/v1/agent_pb'
import * as clipboard from '~/lib/clipboard'
import { ControlRequestActions, ControlRequestContent } from '~/test-support/controlRequestBanner'
import { testControlRequest } from '~/test-support/controlRequests'
import { useTestStorage } from '~/test-support/persistentStorage'
import * as banner from './ControlRequestBanner'
import { useControlResponseHandling } from './controlResponseHandling'
import { questionDecision } from './controls/controlDecision'
import { ControlResponseDeliveryError } from './controls/controlResponseError'
import { createControlAnswerState } from './controls/types'

useTestStorage()

/** A question request with these questions, as the Worker sends it. */
function askRequest(questions: ControlQuestion[], fields: Partial<ControlRequest> = {}): ControlRequest {
  return testControlRequest({
    agentId: 'a1',
    requestId: 'ask-1',
    prompt: { kind: 'question', questions },
    ...fields,
  })
}

/** One question per mode, each with the options One and Two. */
function questionsFor(modes: string[] = ['single']): ControlQuestion[] {
  return modes.map(mode => ({
    question: 'Choose the answer',
    multiSelect: mode === 'multiple',
    options: [
      { label: 'One' },
      { label: 'Two' },
    ],
  }))
}

/** A multiple choice that takes exactly two of three options. */
const LIMITED: ControlQuestion[] = [
  {
    question: 'Choose the answer',
    multiSelect: true,
    minimumSelections: 2,
    maximumSelections: 2,
    options: [
      { label: 'One' },
      { label: 'Two' },
      { label: 'Three' },
    ],
  },
]

/** The question decision of one call of a sender, or a failure for another kind. */
function questionDecided(onDecide: ReturnType<typeof vi.fn<ControlDecisionSender>>, call = 0): QuestionDecision {
  const decision = onDecide.mock.calls[call]?.[0]
  if (decision?.kind.case !== 'question')
    throw new Error(`call ${call} sent no question decision`)
  return decision.kind.value
}

/** The answers of a question decision, as plain values. */
function answersOf(decision: QuestionDecision): Array<{ selected: string[], customText: string }> {
  return decision.answers.map(({ selected, customText }) => ({ selected, customText }))
}

describe('the question selection limits', () => {
  it.each([
    {
      label: 'partial selection',
      selections: ['One'],
      allowed: false,
    },
    {
      label: 'complete selection',
      selections: ['One', 'Two'],
      allowed: true,
    },
    {
      label: 'excessive restored selection',
      selections: ['One', 'Two', 'Three'],
      allowed: false,
    },
  ])('checks the $label through the shared actions', async ({ selections, allowed }) => {
    const request = askRequest(LIMITED)
    const original = structuredClone(request)
    const state = createControlAnswerState({
      selections: { 0: selections },
    })
    const respond = vi.fn<ControlDecisionSender>().mockResolvedValue(undefined)
    render(() => (
      <>
        <ControlRequestContent request={request} answerState={state} />
        <ControlRequestActions request={request} answerState={state} agentProvider={AgentProvider.MUSE_CODE} onDecide={respond} hasEditorContent={false} onTriggerSend={vi.fn()} />
      </>
    ))
    expect(screen.getByText('Select 2 options if you use options.')).toBeInTheDocument()
    const submit = screen.getByTestId('control-submit-btn')
    if (allowed) {
      expect(submit).not.toBeDisabled()
      fireEvent.click(submit)
      await vi.waitFor(() => expect(respond).toHaveBeenCalledOnce())
      expect(answersOf(questionDecided(respond))).toEqual([
        { selected: selections, customText: '' },
      ])
    }
    else {
      expect(submit).toBeDisabled()
      fireEvent.click(submit)
      expect(respond).not.toHaveBeenCalled()
    }
    expect(state.selections()[0]).toEqual(selections)
    expect(request).toEqual(original)
  })

  it('keeps valid selections and the pending request after the Worker refuses the answer', async () => {
    const request = askRequest(LIMITED)
    const original = structuredClone(request)
    const state = createControlAnswerState({
      selections: {
        0: ['One', 'Two'],
      },
    })
    const resetEditor = vi.fn()
    const respond = vi.fn().mockRejectedValue(new ControlResponseDeliveryError(ControlResponseState.READY, 'The question refuses these selections'))
    render(() => {
      const handling = useControlResponseHandling({
        agentId: request.agentId,
        agent: { agentProvider: AgentProvider.MUSE_CODE },
        controlRequests: [request],
        onControlAnswer: respond,
        onSendMessage: vi.fn(),
      }, state, () => undefined, resetEditor)
      return (
        <>
          <ControlRequestContent request={handling.activeControlRequest()} answerState={state} />
          <ControlRequestActions request={handling.activeControlRequest()} answerState={state} agentProvider={AgentProvider.MUSE_CODE} onDecide={handling.decideFor(request)} hasEditorContent={false} onTriggerSend={vi.fn()} />
        </>
      )
    })
    await vi.waitFor(() => expect(state.ready()).toBe(true))
    expect(screen.getByText('Select 2 options if you use options.')).toBeInTheDocument()
    expect(screen.getByRole('checkbox', { name: 'Three' })).toBeDisabled()
    fireEvent.click(screen.getByTestId('control-submit-btn'))
    await vi.waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('The question refuses these selections'))
    expect(state.selections()[0]).toEqual(['One', 'Two'])
    expect(state.responsePending()).toBe(false)
    expect(resetEditor).not.toHaveBeenCalled()
    expect(screen.getByTestId('control-banner')).toBeInTheDocument()
    expect(request).toEqual(original)
  })
})

describe('the question actions', () => {
  it.each([
    {
      label: 'empty text',
      modes: ['single'],
      text: '',
      canSubmit: false,
    },
    {
      label: 'whitespace-only text',
      modes: ['multiple'],
      text: ' \t\n',
      canSubmit: false,
    },
    {
      label: 'typed text',
      modes: ['single'],
      text: '  Native text 界\n',
      canSubmit: true,
    },
    {
      label: 'multiple empty pages',
      modes: ['single', 'multiple'],
      text: '',
      canSubmit: false,
    },
  ])('checks $label through the shared actions', async ({ modes, text, canSubmit }) => {
    const request = askRequest(questionsFor(modes))
    const original = structuredClone(request)
    const state = createControlAnswerState({
      customTexts: { 0: text },
    })
    const respond = vi.fn<ControlDecisionSender>().mockResolvedValue(undefined)
    const triggerSend = vi.fn()
    render(() => <ControlRequestActions request={request} answerState={state} agentProvider={AgentProvider.MUSE_CODE} onDecide={respond} hasEditorContent={false} onTriggerSend={triggerSend} />)
    const submit = screen.getByTestId('control-submit-btn')
    if (!canSubmit) {
      expect(submit).toBeDisabled()
      fireEvent.click(submit)
      expect(respond).not.toHaveBeenCalled()
      expect(triggerSend).not.toHaveBeenCalled()
      expect(state.customTexts()).toEqual({ 0: text })
      expect(state.selections()).toEqual({})
      expect(request).toEqual(original)
      return
    }
    expect(submit).not.toBeDisabled()
    fireEvent.click(submit)
    await vi.waitFor(() => expect(respond).toHaveBeenCalledOnce())
    // The browser sends the text as typed; the Worker trims it as it checks the answer.
    expect(answersOf(questionDecided(respond))).toEqual(modes.map((_, index) => ({
      selected: [],
      customText: index === 0
        ? text
        : '',
    })))
    expect(request).toEqual(original)
  })

  it.each([
    {
      mode: 'single',
      selections: ['One'],
    },
    {
      mode: 'multiple',
      selections: ['One', 'Two'],
    },
  ])('sends the $mode selection', async ({ mode, selections }) => {
    const request = askRequest(questionsFor([mode]))
    const state = createControlAnswerState({
      selections: { 0: selections },
    })
    const respond = vi.fn<ControlDecisionSender>().mockResolvedValue(undefined)
    render(() => <ControlRequestActions request={request} answerState={state} agentProvider={AgentProvider.MUSE_CODE} onDecide={respond} hasEditorContent={false} onTriggerSend={vi.fn()} />)
    fireEvent.click(screen.getByTestId('control-submit-btn'))
    await vi.waitFor(() => expect(respond).toHaveBeenCalledOnce())
    expect(answersOf(questionDecided(respond))).toEqual([
      { selected: selections, customText: '' },
    ])
  })

  it('sends the chosen options and the typed text of a multiple choice together', async () => {
    const request = askRequest(questionsFor(['multiple']))
    const state = createControlAnswerState({
      selections: {
        0: ['One'],
      },
      customTexts: { 0: 'Three' },
    })
    const respond = vi.fn<ControlDecisionSender>().mockResolvedValue(undefined)
    render(() => <ControlRequestActions request={request} answerState={state} agentProvider={AgentProvider.MUSE_CODE} onDecide={respond} hasEditorContent={false} onTriggerSend={vi.fn()} />)
    fireEvent.click(screen.getByTestId('control-submit-btn'))
    await vi.waitFor(() => expect(respond).toHaveBeenCalledOnce())
    expect(answersOf(questionDecided(respond))).toEqual([
      {
        selected: ['One'],
        customText: 'Three',
      },
    ])
  })

  it('keeps explicit cancellation separate from empty text', async () => {
    const request = askRequest(questionsFor())
    const state = createControlAnswerState()
    const respond = vi.fn<ControlDecisionSender>().mockResolvedValue(undefined)
    render(() => <ControlRequestActions request={request} answerState={state} agentProvider={AgentProvider.MUSE_CODE} onDecide={respond} hasEditorContent={false} onTriggerSend={vi.fn()} />)
    fireEvent.click(screen.getByTestId('control-stop-btn'))
    await vi.waitFor(() => expect(respond).toHaveBeenCalledOnce())
    expect(questionDecided(respond)).toMatchObject({ cancelled: true, feedback: 'User stopped', answers: [] })
  })

  it('preserves the empty answer and pending request after delivery refusal', async () => {
    const request = askRequest(questionsFor())
    const original = structuredClone(request)
    const state = createControlAnswerState()
    const resetEditor = vi.fn()
    const respond = vi.fn().mockRejectedValue(new ControlResponseDeliveryError(ControlResponseState.READY, 'The question rejects this answer'))
    let sendAnswer: (() => Promise<void>) | undefined
    render(() => {
      const handling = useControlResponseHandling({
        agentId: request.agentId,
        agent: { agentProvider: AgentProvider.MUSE_CODE },
        controlRequests: [request],
        onControlAnswer: respond,
        onSendMessage: vi.fn(),
      }, state, () => undefined, resetEditor)
      // The shared actions refuse an empty answer, so the test sends one directly.
      sendAnswer = () => handling.decideFor(request)(questionDecision(state, {
        questions: questionsFor(),
      }))
      return (
        <>
          <ControlRequestContent request={handling.activeControlRequest()} answerState={state} />
          <ControlRequestActions request={handling.activeControlRequest()} answerState={state} agentProvider={AgentProvider.MUSE_CODE} onDecide={handling.decideFor(request)} hasEditorContent={false} onTriggerSend={vi.fn()} />
        </>
      )
    })
    await vi.waitFor(() => expect(state.ready()).toBe(true))
    expect(screen.getByTestId('control-submit-btn')).toBeDisabled()
    fireEvent.click(screen.getByTestId('control-submit-btn'))
    expect(respond).not.toHaveBeenCalled()
    expect(sendAnswer).toBeTypeOf('function')
    await expect(sendAnswer!()).rejects.toThrow('The question rejects this answer')
    expect(screen.getByRole('alert')).toHaveTextContent('The question rejects this answer')
    expect(respond).toHaveBeenCalledOnce()
    expect(state.responsePending()).toBe(false)
    expect(state.customTexts()[0] ?? '').toBe('')
    expect(state.selections()).toEqual({})
    expect(resetEditor).not.toHaveBeenCalled()
    expect(screen.getByTestId('control-banner')).toBeInTheDocument()
    expect(screen.getByTestId('control-submit-btn')).toBeDisabled()
    expect(request).toEqual(original)
  })

  it('preserves typed text after delivery refusal and disables a cleared answer', async () => {
    const request = askRequest(questionsFor())
    const state = createControlAnswerState()
    const resetEditor = vi.fn()
    const respond = vi.fn().mockRejectedValue(new ControlResponseDeliveryError(ControlResponseState.READY, 'The question rejects this answer'))
    render(() => {
      const handling = useControlResponseHandling({
        agentId: request.agentId,
        agent: { agentProvider: AgentProvider.MUSE_CODE },
        controlRequests: [request],
        onControlAnswer: respond,
        onSendMessage: vi.fn(),
      }, state, () => undefined, resetEditor)
      return (
        <>
          <ControlRequestContent request={handling.activeControlRequest()} answerState={state} />
          <ControlRequestActions request={handling.activeControlRequest()} answerState={state} agentProvider={AgentProvider.MUSE_CODE} onDecide={handling.decideFor(request)} hasEditorContent={false} onTriggerSend={vi.fn()} />
        </>
      )
    })
    await vi.waitFor(() => expect(state.ready()).toBe(true))
    state.setCustomTexts({ 0: '  Native typed answer 界\n' })
    expect(screen.getByTestId('control-submit-btn')).not.toBeDisabled()
    vi.useFakeTimers({
      toFake: ['setTimeout', 'clearTimeout'],
    })
    try {
      fireEvent.click(screen.getByTestId('control-submit-btn'))
      await vi.waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('The question rejects this answer'))
      expect(respond).toHaveBeenCalledOnce()
      expect(state.responsePending()).toBe(false)
      expect(state.customTexts()[0]).toBe('  Native typed answer 界\n')
      expect(resetEditor).not.toHaveBeenCalled()
      expect(screen.getByTestId('control-banner')).toBeInTheDocument()
      // Complete the actual loading debounce before checking the retry control.
      await vi.runOnlyPendingTimersAsync()
      expect(screen.getByTestId('control-submit-btn')).not.toBeDisabled()
      state.setCustomTexts({ 0: '' })
      expect(screen.getByTestId('control-submit-btn')).toBeDisabled()
      fireEvent.click(screen.getByTestId('control-submit-btn'))
      expect(respond).toHaveBeenCalledOnce()
    }
    finally {
      vi.useRealTimers()
    }
  })
})

// The Worker refuses an answer that the agent's request cannot take, such as one whose
// native choices it cannot read. The request stays pending, so the reader keeps every
// way to deal with it: the typed text, the raw bytes, and the stop.
describe('a pending request whose answer the Worker refuses', () => {
  it('keeps the request usable', async () => {
    const original = '{"method":"approval/requested","params":{"availableChoices":0},"futureNumber":9007199254740993}'
    const request = testControlRequest({
      agentId: 'muse-agent',
      requestId: 'native-pending',
      payload: JSON.parse(original),
      originalPayload: new TextEncoder().encode(original),
    })
    const state = createControlAnswerState()
    const interrupt = vi.fn()
    const resetEditor = vi.fn()
    const respond = vi.fn().mockRejectedValue(new ControlResponseDeliveryError(ControlResponseState.READY, 'LeapMux cannot read this answer'))
    const copy = vi.spyOn(clipboard, 'copyTextToClipboard').mockResolvedValue(true)
    try {
      render(() => {
        const handling = useControlResponseHandling({
          agentId: request.agentId,
          agent: { agentProvider: AgentProvider.MUSE_CODE },
          controlRequests: [request],
          onControlAnswer: respond,
          onSendMessage: vi.fn(),
        }, state, () => undefined, resetEditor)
        return (
          <>
            <ControlRequestContent request={handling.activeControlRequest()} answerState={state} onInterrupt={interrupt} />
            <ControlRequestActions request={handling.activeControlRequest()} answerState={state} agentProvider={AgentProvider.MUSE_CODE} onDecide={handling.decideFor(request)} hasEditorContent={false} onTriggerSend={vi.fn()} />
          </>
        )
      })
      await vi.waitFor(() => expect(screen.getByTestId('control-deny-btn')).not.toBeDisabled())
      state.setCustomTexts({ 0: 'Keep the pending answer' })
      fireEvent.click(screen.getByTestId('control-deny-btn'))
      await vi.waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('LeapMux cannot read this answer'))
      expect(respond).toHaveBeenCalledOnce()
      expect(state.responsePending()).toBe(false)
      expect(state.customTexts()).toEqual({ 0: 'Keep the pending answer' })
      expect(resetEditor).not.toHaveBeenCalled()
      expect(screen.getByTestId('control-banner')).toBeInTheDocument()
      fireEvent.click(screen.getByTestId('control-copy-json'))
      await vi.waitFor(() => expect(copy).toHaveBeenCalledOnce())
      expect(copy.mock.calls[0]?.[0]).toContain('9007199254740993')
      expect(copy.mock.calls[0]?.[0]).toContain('approval/requested')
      expect(request.originalPayload).toEqual(new TextEncoder().encode(original))
      fireEvent.click(screen.getByTestId('control-interrupt'))
      expect(interrupt).toHaveBeenCalledOnce()
    }
    finally {
      copy.mockRestore()
    }
  })
})

it.each([
  { state: ControlResponseState.DELIVERED, action: 'Save response', notice: 'The response action is complete. Save its transcript entry without repeating the action.' },
  { state: ControlResponseState.UNCERTAIN, action: 'Check status', notice: 'LeapMux cannot confirm response delivery. It will not send another response.' },
  { state: ControlResponseState.PENDING, action: 'Check status', notice: 'LeapMux has a saved response but no delivery receipt yet.' },
  { state: ControlResponseState.UNSPECIFIED, action: 'Check status', notice: 'Check the response state before sending an answer.' },
])('uses a recording-only action for response state $state', async ({ state, action, notice }) => {
  const request: ControlRequest = {
    ...questionRequest(),
    responseState: state,
  }
  const answerState = createControlAnswerState()
  const respond = vi.fn<ControlDecisionSender>().mockResolvedValue(undefined)
  const record = vi.fn().mockResolvedValue(undefined)
  render(() => (
    <>
      <ControlRequestContent request={request} answerState={answerState} />
      <ControlRequestActions request={request} answerState={answerState} agentProvider={AgentProvider.CLAUDE_CODE} onDecide={respond} onRecordResponse={record} hasEditorContent={false} onTriggerSend={() => {}} />
    </>
  ))
  expect(screen.getByRole('status')).toHaveTextContent(notice)
  expect(screen.queryByTestId('control-allow-btn')).not.toBeInTheDocument()
  expect(screen.queryByTestId('control-deny-btn')).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: action }))
  await vi.waitFor(() => expect(record).toHaveBeenCalledOnce())
  expect(respond).not.toHaveBeenCalled()
  answerState.setResponsePending(true)
  expect(screen.getByRole('button', { name: action })).toBeDisabled()
})

it('copies native control JSON without rounding numeric literals or losing repeated keys', async () => {
  const original = '{"id":9007199254740993,"request":{"tool_name":"Bash","input":{"value":1,"value":2,"large":9007199254740993}}}'
  const request = testControlRequest({
    requestId: 'request-1',
    agentId: 'agent-1',
    payload: JSON.parse(original),
    originalPayload: new TextEncoder().encode(original),
  })
  const copy = vi.spyOn(clipboard, 'copyTextToClipboard').mockResolvedValue(true)
  try {
    const { getByTestId } = render(() => <ControlRequestContent request={request} answerState={createControlAnswerState()} />)
    fireEvent.click(getByTestId('control-copy-json'))
    await vi.waitFor(() => expect(copy).toHaveBeenCalledOnce())
    // The wait above guarantees the call; `?.` is the type-level guard alone.
    expect(copy.mock.calls[0]?.[0]).toContain('9007199254740993')
    expect(copy.mock.calls[0]?.[0]).toMatch(/"value"\s*:\s*1/)
    expect(copy.mock.calls[0]?.[0]).toMatch(/"value"\s*:\s*2/)
  }
  finally {
    copy.mockRestore()
  }
})

it.each([
  {
    original: new TextEncoder().encode('{unfinished'),
    expected: '{unfinished',
  },
  {
    original: new Uint8Array([0xFF, 0xFE]),
    expected: '//4=',
  },
])('preserves unavailable JSON in the copy action: $expected', async ({ original, expected }) => {
  const request = testControlRequest({
    requestId: 'request-1',
    agentId: 'agent-1',
    payload: {
      request: { tool_name: 'Bash' },
    },
    originalPayload: original,
  })
  const copy = vi.spyOn(clipboard, 'copyTextToClipboard').mockResolvedValue(true)
  try {
    const { getByTestId } = render(() => <ControlRequestContent request={request} answerState={createControlAnswerState()} />)
    fireEvent.click(getByTestId('control-copy-json'))
    await vi.waitFor(() => expect(copy).toHaveBeenCalledOnce())
    // The wait above guarantees the call; `?.` is the type-level guard alone.
    expect(copy.mock.calls[0]?.[0]).toContain(expected)
  }
  finally {
    copy.mockRestore()
  }
})

function planRequest(): ControlRequest {
  return testControlRequest({
    requestId: 'plan-1',
    agentId: 'a1',
    prompt: { kind: 'plan' },
  })
}

function questionRequest(): ControlRequest {
  return askRequest([
    {
      question: 'Which database?',
      options: [
        { label: 'Postgres' },
        { label: 'MySQL' },
      ],
    },
  ])
}

// The banner classifies nothing. Its caller passes the prompt as the surface, so the
// SAME request draws a different control when the surface says so. These cases are
// what prove the component reads the prop rather than the request.
describe('ControlRequestBanner takes the surface from its caller', () => {
  it('offers no invented decision for an empty independent option list', () => {
    render(() => (
      <banner.ControlRequestActions
        request={testControlRequest({ agentId: 'agent', requestId: 'choice' })}
        controlSurface={{
          kind: 'permission',
          permission: { options: [], independentOptions: true },
        }}
        answerState={createControlAnswerState()}
        hasEditorContent={false}
        onDecide={vi.fn()}
        onTriggerSend={vi.fn()}
      />
    ))
    expect(screen.queryAllByRole('button')).toHaveLength(0)
  })

  it('draws the question form when the surface says question', () => {
    const request = questionRequest()
    render(() => (
      <banner.ControlRequestContent
        request={request}
        controlSurface={request.prompt}
        answerState={createControlAnswerState()}
      />
    ))
    expect(screen.getByText('Which database?')).toBeVisible()
  })

  // The SURFACE decides, not the request. The same request draws a question where the
  // surface says question and a permission where it says permission, which is what
  // keeps one prompt in charge of both halves of the banner.
  it('draws the permission content for the same request when the surface says permission', () => {
    render(() => (
      <banner.ControlRequestContent
        request={questionRequest()}
        controlSurface={{
          kind: 'permission',
          permission: { options: [] },
        }}
        answerState={createControlAnswerState()}
      />
    ))
    expect(screen.getByTestId('control-banner')).toBeInTheDocument()
    expect(screen.queryByText('Which database?')).not.toBeInTheDocument()
  })

  it('draws the permission actions for the same request when the surface says permission', () => {
    render(() => (
      <banner.ControlRequestActions
        request={questionRequest()}
        controlSurface={{
          kind: 'permission',
          permission: { options: [] },
        }}
        answerState={createControlAnswerState()}
        agentProvider={AgentProvider.CLAUDE_CODE}
        onDecide={vi.fn<ControlDecisionSender>().mockResolvedValue(undefined)}
        hasEditorContent={false}
        onTriggerSend={() => {}}
      />
    ))
    expect(screen.queryByTestId('control-submit-btn')).not.toBeInTheDocument()
  })

  // The agent's turn blocks until an answer reaches it, so a confirm dialog draws its
  // own pair of answers whatever the provider.
  it('draws the pair of a confirm dialog', () => {
    render(() => (
      <banner.ControlRequestActions
        request={questionRequest()}
        controlSurface={{
          kind: 'dialog',
          dialog: { title: 'Pick a branch', variant: 'confirm' },
        }}
        answerState={createControlAnswerState()}
        agentProvider={AgentProvider.CLAUDE_CODE}
        onDecide={vi.fn<ControlDecisionSender>().mockResolvedValue(undefined)}
        hasEditorContent={false}
        onTriggerSend={() => {}}
      />
    ))
    expect(screen.getByTestId('control-deny-btn')).toBeInTheDocument()
    expect(screen.getByTestId('control-allow-btn')).toBeInTheDocument()
  })

  // The surface gives the dialog, and the Worker's provider turns the decision into
  // the runtime's own answer.
  it('sends the decision of an input dialog', async () => {
    const onRespond = vi.fn<ControlDecisionSender>().mockResolvedValue(undefined)
    render(() => (
      <banner.ControlRequestActions
        request={testControlRequest({
          requestId: 'dialog-1',
          agentId: 'a1',
          payload: {
            type: 'extension_ui_request',
            id: 'dialog-1',
            method: 'input',
            title: 'Pick a branch',
          },
        })}
        controlSurface={{
          kind: 'dialog',
          dialog: { title: 'Pick a branch', prefill: 'main', variant: 'input' },
        }}
        answerState={createControlAnswerState()}
        agentProvider={AgentProvider.PI}
        onDecide={onRespond}
        hasEditorContent={false}
        onTriggerSend={() => {}}
      />
    ))
    expect(screen.getByTestId('dialog-input')).toHaveValue('main')
    expect(screen.getByTestId('control-deny-btn')).toHaveTextContent('Cancel')
    fireEvent.click(screen.getByTestId('control-allow-btn'))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    const decision = onRespond.mock.calls[0]?.[0]
    expect(decision?.kind.case === 'dialog'
      ? decision.kind.value
      : undefined).toMatchObject({ action: DialogAction.SUBMIT, value: 'main' })
  })

  // The plan surface states the extra answers, and the banner hands them to the plan
  // actions whatever the provider.
  it('offers the choices of a plan surface in the overflow menu', () => {
    render(() => (
      <banner.ControlRequestActions
        request={planRequest()}
        controlSurface={{
          kind: 'plan',
          choices: [
            { id: 'Revise', label: 'Request revisions', effect: 'refuse' },
          ],
        }}
        answerState={createControlAnswerState()}
        agentProvider={AgentProvider.CLAUDE_CODE}
        onDecide={vi.fn<ControlDecisionSender>().mockResolvedValue(undefined)}
        hasEditorContent={false}
        onTriggerSend={() => {}}
      />
    ))
    fireEvent.click(screen.getByTestId('control-more-actions'))
    expect(screen.getByTestId('plan-choice-0')).toHaveTextContent('Request revisions')
  })
})

/**
 * A caller CAN pass `request` as a REACTIVE prop, and a store removal then
 * turns it null under a mounted banner. These tests own that hazard because
 * they render one half as the ROOT.
 *
 * The harness reads the surface OUTSIDE the `<Show when={props.request}>` of
 * each half, which is what the composer does, so the removal reaches it with
 * no request rather than a disposed memo, and it reads no surface for that
 * request. `AgentEditorPanel` keys its owner on the request and never passes a
 * reactive one.
 *
 * `BannerContentProps` and `BannerActionsProps` are what let these tests pass
 * `null` at all. The shared `ContentProps` / `ActionsProps` that a provider
 * plugin takes keep the request non-null, because the banner renders a plugin
 * only inside a `<Show>` that already proved it.
 */
describe('ControlRequestBanner reactive request removal', () => {
  it('removes the content after its reactive request becomes null', () => {
    const [request, setRequest] = createSignal<ControlRequest | null>(planRequest())
    render(() => (
      <ControlRequestContent
        request={request()}
        answerState={createControlAnswerState()}
      />
    ))

    expect(screen.getByTestId('control-banner')).toBeInTheDocument()
    expect(() => setRequest(null)).not.toThrow()
    expect(screen.queryByTestId('control-banner')).not.toBeInTheDocument()
  })

  it('removes the actions after their reactive request becomes null', () => {
    const [request, setRequest] = createSignal<ControlRequest | null>(planRequest())
    render(() => (
      <ControlRequestActions
        request={request()}
        answerState={createControlAnswerState()}
        agentProvider={AgentProvider.CLAUDE_CODE}
        onDecide={vi.fn<ControlDecisionSender>().mockResolvedValue(undefined)}
        hasEditorContent={false}
        onTriggerSend={() => {}}
      />
    ))

    expect(screen.getByTestId('plan-approve-btn')).toBeInTheDocument()
    expect(() => setRequest(null)).not.toThrow()
    expect(screen.queryByTestId('plan-approve-btn')).not.toBeInTheDocument()
  })

  // The question match takes one prompt kind and not the other. A swap between
  // the two kinds therefore exercises both of its branches. The removal between
  // the two writes also makes the match run once with no request at all.
  it('switches the content between a question and a plan across a removal', () => {
    const [request, setRequest] = createSignal<ControlRequest | null>(questionRequest())
    render(() => (
      <ControlRequestContent
        request={request()}
        answerState={createControlAnswerState()}
      />
    ))

    expect(screen.getByTestId('control-banner')).toHaveTextContent('Which database?')

    expect(() => {
      setRequest(null)
      setRequest(planRequest())
    }).not.toThrow()

    expect(screen.getByTestId('control-banner')).toHaveTextContent('Plan Ready for Review')
    expect(screen.queryByText('Which database?')).not.toBeInTheDocument()
  })

  it('switches the actions between a question and a plan across a removal', () => {
    const [request, setRequest] = createSignal<ControlRequest | null>(planRequest())
    render(() => (
      <ControlRequestActions
        request={request()}
        answerState={createControlAnswerState()}
        agentProvider={AgentProvider.CLAUDE_CODE}
        onDecide={vi.fn<ControlDecisionSender>().mockResolvedValue(undefined)}
        hasEditorContent={false}
        onTriggerSend={() => {}}
      />
    ))

    expect(screen.getByTestId('plan-approve-btn')).toBeInTheDocument()

    expect(() => {
      setRequest(null)
      setRequest(questionRequest())
    }).not.toThrow()

    expect(screen.queryByTestId('plan-approve-btn')).not.toBeInTheDocument()
    expect(screen.getByTestId('control-submit-btn')).toBeInTheDocument()
  })

  // Its own case, so a metrics regression names the metrics. Folded into the
  // removal test above, it failed under a name that sent the reader to the
  // request lifecycle instead.
  it('uses Oat small metrics for question actions', () => {
    render(() => (
      <ControlRequestActions
        request={questionRequest()}
        answerState={createControlAnswerState()}
        agentProvider={AgentProvider.CLAUDE_CODE}
        onDecide={vi.fn<ControlDecisionSender>().mockResolvedValue(undefined)}
        hasEditorContent={false}
        onTriggerSend={() => {}}
      />
    ))

    expect(screen.getByTestId('control-stop-btn')).toHaveClass('outline', compactControl)
    expect(screen.getByTestId('control-yolo-btn')).toHaveClass('outline', compactControl)
    expect(screen.getByTestId('control-submit-btn')).toHaveClass(compactControl)
    expect(screen.getByTestId('control-submit-btn')).not.toHaveClass('outline')
  })
})

// A turn blocked on a question is one the reader may want to abandon rather than
// answer. Denying is an answer: it reaches the agent, which carries on with something
// else. The stop lives with the QUESTION, away from the decisions, so it cannot be
// pressed for one.
describe('a request whose payload LeapMux cannot read', () => {
  const faulted = (fault: 'malformed' | 'not-an-object'): ControlRequest => testControlRequest({
    agentId: 'agent-1',
    requestId: 'request-1',
    editorRole: 'none',
    payloadFault: fault,
    originalPayload: new TextEncoder().encode('{not json'),
  })

  // A request with no fields reads as "Permission Required", and that sentence states
  // what the agent asked for. Here nobody knows, so the banner says so instead.
  it.each([
    ['malformed', 'LeapMux cannot read this request. The agent sent bytes that are not JSON.'],
    ['not-an-object', 'LeapMux cannot read this request. The agent sent JSON that is not an object.'],
  ] as const)('states the %s fault rather than a question it cannot read', (fault, notice) => {
    render(() => (
      <ControlRequestContent
        request={faulted(fault)}
        answerState={createControlAnswerState()}
      />
    ))
    expect(screen.getByRole('alert')).toHaveTextContent(notice)
    expect(screen.queryByText('Permission Required')).not.toBeInTheDocument()
  })

  // Allow is the dangerous one: it grants something nobody can read. Deny is no better
  // here, because the Worker refuses every answer to a request it cannot read. The stop
  // releases the agent without an answer, and the worker cancels from its own copy of
  // the bytes. Every provider, because the rule is the banner's own and a new provider
  // must not reach a decision for bytes that nobody can read.
  it.each(ALL_PROVIDERS)(
    'offers no decision for provider %s',
    (provider) => {
      render(() => (
        <ControlRequestActions
          request={faulted('malformed')}
          answerState={createControlAnswerState()}
          agentProvider={provider}
          onDecide={vi.fn()}
          hasEditorContent={false}
          onTriggerSend={vi.fn()}
        />
      ))
      expect(screen.queryByTestId('control-allow-btn')).not.toBeInTheDocument()
      expect(screen.queryByTestId('control-deny-btn')).not.toBeInTheDocument()
      expect(screen.queryAllByRole('button')).toHaveLength(0)
    },
  )

  it('keeps the stop, which is the only way left to release the agent', () => {
    const interrupt = vi.fn()
    render(() => (
      <ControlRequestContent
        request={faulted('malformed')}
        answerState={createControlAnswerState()}
        onInterrupt={interrupt}
      />
    ))
    fireEvent.click(screen.getByTestId('control-interrupt'))
    expect(interrupt).toHaveBeenCalledOnce()
  })

  it('still copies the bytes that arrived, so the fault can be diagnosed', async () => {
    const copy = vi.spyOn(clipboard, 'copyTextToClipboard').mockResolvedValue(true)
    render(() => (
      <ControlRequestContent
        request={faulted('malformed')}
        answerState={createControlAnswerState()}
      />
    ))
    fireEvent.click(screen.getByTestId('control-copy-json'))
    await vi.waitFor(() => expect(copy).toHaveBeenCalledOnce())
    // The wait above guarantees the call; `?.` is the type-level guard alone.
    expect(copy.mock.calls[0]?.[0]).toContain('{not json')
    copy.mockRestore()
  })
})

describe('interrupting a turn that is waiting on an answer', () => {
  it('offers the stop beside the request rather than among its decisions', () => {
    const interrupt = vi.fn()
    render(() => (
      <ControlRequestContent
        request={questionRequest()}
        answerState={createControlAnswerState()}
        onInterrupt={interrupt}
      />
    ))
    const stop = screen.getByTestId('control-interrupt')
    expect(screen.getByTestId('control-banner-actions')).toContainElement(stop)
    fireEvent.click(stop)
    expect(interrupt).toHaveBeenCalledOnce()
  })

  // A subagent whose provider cannot interrupt one conversation passes none, and the
  // banner then offers no control that would fail.
  it('offers no stop when this agent cannot be interrupted', () => {
    render(() => (
      <ControlRequestContent
        request={questionRequest()}
        answerState={createControlAnswerState()}
      />
    ))
    expect(screen.queryByTestId('control-interrupt')).not.toBeInTheDocument()
  })
})

// A disabled control dispatches no pointer event and takes no focus, so the
// offscreen description is the only route to a screen-reader user there. The
// parity census read the disabled submit as "the options are the only answer"
// and reported the typed answer as unreachable; the reason now says otherwise.
it('describes what the disabled submit waits for, and drops the description once answered', () => {
  const request = questionRequest()
  const answerState = createControlAnswerState()
  render(() => (
    <ControlRequestActions request={request} answerState={answerState} agentProvider={AgentProvider.CLAUDE_CODE} onDecide={vi.fn()} hasEditorContent={false} onTriggerSend={() => {}} />
  ))
  const submit = screen.getByTestId('control-submit-btn')
  expect(submit).toBeDisabled()
  const described = submit.getAttribute('aria-describedby')
  expect(described).toBeTruthy()
  expect(document.getElementById(described!)).toHaveTextContent('Choose an option, or type a custom answer below.')

  answerState.setSelections({
    0: ['Postgres'],
  })
  expect(submit).toBeEnabled()
  expect(submit).not.toHaveAttribute('aria-describedby')
})

// Composer text answers the question, so the button and its reason must agree:
// a submit the reader can press states nothing about being blocked.
it('states no reason while unsaved composer text answers the question', () => {
  const request = questionRequest()
  render(() => (
    <ControlRequestActions request={request} answerState={createControlAnswerState()} agentProvider={AgentProvider.CLAUDE_CODE} onDecide={vi.fn()} hasEditorContent={true} onTriggerSend={() => {}} />
  ))
  const submit = screen.getByTestId('control-submit-btn')
  expect(submit).toBeEnabled()
  expect(submit).not.toHaveAttribute('aria-describedby')
})
