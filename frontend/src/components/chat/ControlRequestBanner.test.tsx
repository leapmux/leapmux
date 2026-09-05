import type { ControlRequest } from '~/stores/control.store'
import { render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { createAskQuestionState } from '~/test-support/askQuestionState'
import { ControlRequestActions, ControlRequestContent } from './ControlRequestBanner'
import './providers'

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

/**
 * Both components detect an AskUserQuestion payload in a memo in their BODY,
 * beside the `<Show when={props.request}>` that renders the rest. A memo there
 * is not a descendant of that Show, so the Show cannot dispose it first: a
 * caller that passes `request` as a REACTIVE prop re-runs the memo with the
 * removed request still in place, and the memo dereferences it.
 *
 * These tests own that hazard because they render the component as the ROOT.
 * The same removal through `AgentEditorPanel` cannot reach the memo -- the
 * render effect that inserts the component is a stale ancestor there, so Solid
 * disposes the whole component before the pure phase reaches its body. The
 * panel is why the `!` below is a deliberate contract violation and not an
 * oversight: it passes a non-null `request` by keying the owner on it, and
 * these tests state what the components do for a caller that does not.
 */
describe('controlRequestBanner reactive request removal', () => {
  it('removes the content after its reactive request becomes null', () => {
    const [request, setRequest] = createSignal<ControlRequest | null>(planRequest())
    render(() => (
      <ControlRequestContent
        request={request()!}
        askState={createAskQuestionState()}
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
        request={request()!}
        askState={createAskQuestionState()}
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
  // other, so a swap between them exercises both of its branches -- and the
  // removal in between makes it run once with no request at all.
  it('switches the content between a question and a plan across a removal', () => {
    const [request, setRequest] = createSignal<ControlRequest | null>(questionRequest())
    render(() => (
      <ControlRequestContent
        request={request()!}
        askState={createAskQuestionState()}
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
        request={request()!}
        askState={createAskQuestionState()}
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
})
