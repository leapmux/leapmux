import type { ControlRequest } from '~/stores/control.store'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { compactControl } from '~/components/common/CompactControl.css'
import { AgentProvider, ControlResponseState } from '~/generated/proto/leapmux/v1/agent_pb'
import * as clipboard from '~/lib/clipboard'
import { ControlRequestActions, ControlRequestContent } from '~/test-support/controlRequestBanner'
import * as banner from './ControlRequestBanner'
import { controlSurface } from './controls/controlSurface'
import { createControlAnswerState } from './controls/types'
import './providers'

it.each([
  { state: ControlResponseState.DELIVERED, action: 'Save response', notice: 'The response action is complete. Save its transcript entry without repeating the action.' },
  { state: ControlResponseState.UNCERTAIN, action: 'Check status', notice: 'LeapMux cannot confirm response delivery. It will not send another response.' },
  { state: ControlResponseState.PENDING, action: 'Check status', notice: 'LeapMux has a saved response but no delivery receipt yet.' },
  { state: ControlResponseState.UNSPECIFIED, action: 'Check status', notice: 'Check the response state before sending an answer.' },
])('uses a recording-only action for response state $state', async ({ state, action, notice }) => {
  const request: ControlRequest = { ...questionRequest(), responseState: state }
  const answerState = createControlAnswerState()
  const respond = vi.fn().mockResolvedValue(undefined)
  const record = vi.fn().mockResolvedValue(undefined)
  render(() => (
    <>
      <ControlRequestContent request={request} answerState={answerState} agentProvider={AgentProvider.CLAUDE_CODE} />
      <ControlRequestActions request={request} answerState={answerState} agentProvider={AgentProvider.CLAUDE_CODE} onRespond={respond} onRecordResponse={record} hasEditorContent={false} onTriggerSend={() => {}} />
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
  const request = { requestId: 'request-1', agentId: 'agent-1', payload: JSON.parse(original), originalPayload: new TextEncoder().encode(original) }
  const copy = vi.spyOn(clipboard, 'copyTextToClipboard').mockResolvedValue(true)
  try {
    const { getByTestId } = render(() => <ControlRequestContent request={request} answerState={createControlAnswerState()} agentProvider={AgentProvider.CLAUDE_CODE} />)
    fireEvent.click(getByTestId('control-copy-json'))
    await vi.waitFor(() => expect(copy).toHaveBeenCalledOnce())
    expect(copy.mock.calls[0][0]).toContain('9007199254740993')
    expect(copy.mock.calls[0][0]).toMatch(/"value"\s*:\s*1/)
    expect(copy.mock.calls[0][0]).toMatch(/"value"\s*:\s*2/)
  }
  finally {
    copy.mockRestore()
  }
})

it.each([
  { original: new TextEncoder().encode('{unfinished'), expected: '{unfinished' },
  { original: new Uint8Array([0xFF, 0xFE]), expected: '//4=' },
])('preserves unavailable JSON in the copy action: $expected', async ({ original, expected }) => {
  const request = { requestId: 'request-1', agentId: 'agent-1', payload: { request: { tool_name: 'Bash' } }, originalPayload: original }
  const copy = vi.spyOn(clipboard, 'copyTextToClipboard').mockResolvedValue(true)
  try {
    const { getByTestId } = render(() => <ControlRequestContent request={request} answerState={createControlAnswerState()} agentProvider={AgentProvider.CLAUDE_CODE} />)
    fireEvent.click(getByTestId('control-copy-json'))
    await vi.waitFor(() => expect(copy).toHaveBeenCalledOnce())
    expect(copy.mock.calls[0][0]).toContain(expected)
  }
  finally {
    copy.mockRestore()
  }
})

function planRequest(): ControlRequest {
  return {
    requestId: 'plan-1',
    agentId: 'a1',
    payload: {
      request: { tool_name: 'ExitPlanMode', input: {} },
    },
  }
}

function questionRequest(): ControlRequest {
  return {
    requestId: 'ask-1',
    agentId: 'a1',
    payload: {
      request: {
        tool_name: 'AskUserQuestion',
        input: {
          questions: [{ question: 'Which database?', options: [{ label: 'Postgres' }, { label: 'MySQL' }] }],
        },
      },
    },
  }
}

// The banner classifies nothing. Its caller derives the surface and passes it,
// so the SAME payload draws a different control when the surface says so. These
// two cases are what prove the component reads the prop rather than the payload.
describe('controlRequestBanner takes the surface from its caller', () => {
  it('draws the question form when the surface says question', () => {
    const request = questionRequest()
    render(() => (
      <banner.ControlRequestContent
        request={request}
        controlSurface={controlSurface(request, AgentProvider.CLAUDE_CODE, undefined)}
        answerState={createControlAnswerState()}
        agentProvider={AgentProvider.CLAUDE_CODE}
      />
    ))
    expect(screen.getByText('Which database?')).toBeVisible()
  })

  it('draws the plugin content for the same payload when the surface says plugin', () => {
    render(() => (
      <banner.ControlRequestContent
        request={questionRequest()}
        controlSurface={{ kind: 'plugin' }}
        answerState={createControlAnswerState()}
        agentProvider={AgentProvider.CLAUDE_CODE}
      />
    ))
    expect(screen.getByTestId('control-banner')).toBeInTheDocument()
    expect(screen.queryByText('Which database?')).not.toBeInTheDocument()
  })

  it('draws the plugin actions for the same payload when the surface says plugin', () => {
    render(() => (
      <banner.ControlRequestActions
        request={questionRequest()}
        controlSurface={{ kind: 'plugin' }}
        answerState={createControlAnswerState()}
        agentProvider={AgentProvider.CLAUDE_CODE}
        onRespond={vi.fn().mockResolvedValue(undefined)}
        hasEditorContent={false}
        onTriggerSend={() => {}}
      />
    ))
    expect(screen.queryByTestId('control-submit-btn')).not.toBeInTheDocument()
  })
})

/**
 * A caller CAN pass `request` as a REACTIVE prop, and a store removal then
 * turns it null under a mounted banner. These tests own that hazard because
 * they render one half as the ROOT.
 *
 * The harness derives the surface OUTSIDE the `<Show when={props.request}>` of
 * each half, which is what the composer does, so the removal reaches
 * `controlSurface` with no request rather than a disposed memo. It returns
 * nothing for that request instead of dereferencing it. `AgentEditorPanel`
 * keys its owner on the request and never passes a reactive one.
 *
 * `BannerContentProps` and `BannerActionsProps` are what let these tests pass
 * `null` at all. The shared `ContentProps` / `ActionsProps` that a provider
 * plugin takes keep the request non-null, because the banner renders a plugin
 * only inside a `<Show>` that already proved it.
 */
describe('controlRequestBanner reactive request removal', () => {
  it('removes the content after its reactive request becomes null', () => {
    const [request, setRequest] = createSignal<ControlRequest | null>(planRequest())
    render(() => (
      <ControlRequestContent
        request={request()}
        answerState={createControlAnswerState()}
        agentProvider={AgentProvider.CLAUDE_CODE}
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
        onRespond={vi.fn().mockResolvedValue(undefined)}
        hasEditorContent={false}
        onTriggerSend={() => {}}
      />
    ))

    expect(screen.getByTestId('plan-approve-btn')).toBeInTheDocument()
    expect(() => setRequest(null)).not.toThrow()
    expect(screen.queryByTestId('plan-approve-btn')).not.toBeInTheDocument()
  })

  // The question memo returns a value for one payload shape and nothing for the
  // other. A swap between the two shapes therefore exercises both of its
  // branches. The removal between the two writes also makes the memo run once
  // with no request at all.
  it('switches the content between a question and a plan across a removal', () => {
    const [request, setRequest] = createSignal<ControlRequest | null>(questionRequest())
    render(() => (
      <ControlRequestContent
        request={request()}
        answerState={createControlAnswerState()}
        agentProvider={AgentProvider.CLAUDE_CODE}
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
        onRespond={vi.fn().mockResolvedValue(undefined)}
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
        onRespond={vi.fn().mockResolvedValue(undefined)}
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
  const faulted = (fault: 'malformed' | 'not-an-object'): ControlRequest => ({
    agentId: 'agent-1',
    requestId: 'request-1',
    payload: {},
    payloadFault: fault,
    originalPayload: new TextEncoder().encode('{not json'),
  })

  // The provider plugins all draw "Permission Required" from an empty payload, because an
  // empty object is what a permission request looks like once its fields are gone. That
  // sentence states what the agent asked for, and here nobody knows. Say so instead.
  it.each([
    ['malformed', 'LeapMux cannot read this request. The agent sent bytes that are not JSON.'],
    ['not-an-object', 'LeapMux cannot read this request. The agent sent JSON that is not an object.'],
  ] as const)('states the %s fault rather than a question it cannot read', (fault, notice) => {
    render(() => (
      <ControlRequestContent
        request={faulted(fault)}
        answerState={createControlAnswerState()}
        agentProvider={AgentProvider.CLAUDE_CODE}
      />
    ))
    expect(screen.getByRole('alert')).toHaveTextContent(notice)
    expect(screen.queryByText('Permission Required')).not.toBeInTheDocument()
  })

  // Allow is the dangerous one: it grants something nobody can read. Deny is no better
  // here, because composing one needs the option list or decision vocabulary the payload
  // was carrying -- so LeapMux can build no valid answer at all. The stop releases the
  // agent without an answer, and the worker cancels from its own copy of the bytes.
  it.each([AgentProvider.CLAUDE_CODE, AgentProvider.CODEX, AgentProvider.OPENCODE, AgentProvider.GITHUB_COPILOT, AgentProvider.PI, AgentProvider.ZCODE])(
    'offers no decision for provider %s',
    (provider) => {
      render(() => (
        <ControlRequestActions
          request={faulted('malformed')}
          answerState={createControlAnswerState()}
          agentProvider={provider}
          onRespond={vi.fn()}
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
        agentProvider={AgentProvider.CLAUDE_CODE}
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
        agentProvider={AgentProvider.CLAUDE_CODE}
      />
    ))
    fireEvent.click(screen.getByTestId('control-copy-json'))
    await vi.waitFor(() => expect(copy).toHaveBeenCalledOnce())
    expect(copy.mock.calls[0][0]).toContain('{not json')
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
        agentProvider={AgentProvider.CLAUDE_CODE}
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
        agentProvider={AgentProvider.CLAUDE_CODE}
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
    <ControlRequestActions request={request} answerState={answerState} agentProvider={AgentProvider.CLAUDE_CODE} onRespond={vi.fn()} hasEditorContent={false} onTriggerSend={() => {}} />
  ))
  const submit = screen.getByTestId('control-submit-btn')
  expect(submit).toBeDisabled()
  const described = submit.getAttribute('aria-describedby')
  expect(described).toBeTruthy()
  expect(document.getElementById(described!)).toHaveTextContent('Choose an option, or type a custom answer below.')

  answerState.setSelections({ 0: ['Postgres'] })
  expect(submit).toBeEnabled()
  expect(submit).not.toHaveAttribute('aria-describedby')
})

// Composer text answers the question, so the button and its reason must agree:
// a submit the reader can press states nothing about being blocked.
it('states no reason while unsaved composer text answers the question', () => {
  const request = questionRequest()
  render(() => (
    <ControlRequestActions request={request} answerState={createControlAnswerState()} agentProvider={AgentProvider.CLAUDE_CODE} onRespond={vi.fn()} hasEditorContent={true} onTriggerSend={() => {}} />
  ))
  const submit = screen.getByTestId('control-submit-btn')
  expect(submit).toBeEnabled()
  expect(submit).not.toHaveAttribute('aria-describedby')
})
