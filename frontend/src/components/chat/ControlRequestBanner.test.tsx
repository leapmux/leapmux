import type { ControlRequest } from '~/stores/control.store'
import { render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { ControlRequestActions, ControlRequestContent } from './ControlRequestBanner'
import { createControlAnswerState } from './controls/types'
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
 * is not a descendant of that Show, so the Show cannot dispose it first. A
 * caller that passes `request` as a REACTIVE prop re-runs the memo with the
 * removed request. `controlQuestion` returns nothing for that request rather
 * than dereferencing it.
 *
 * These tests own that hazard because they render the component as the ROOT.
 * The same removal through `AgentEditorPanel` cannot reach the memo, because
 * the panel keys its owner on the request: `request` arrives there as a plain
 * value, so the memo has no reactive source and never re-runs.
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
})
