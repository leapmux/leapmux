import type { ControlRequest } from '~/stores/control.store'
import { render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { createAskQuestionState } from '~/test-support/askQuestionState'
import { ControlRequestActions, ControlRequestContent } from './ControlRequestBanner'
import './providers'

function makeRequest(): ControlRequest {
  return {
    requestId: 'plan-1',
    agentId: 'a1',
    payload: {
      request: { tool_name: 'ExitPlanMode', input: {} },
    },
  }
}

describe('controlRequestBanner request removal', () => {
  it('removes the content after its reactive request becomes null', () => {
    const [request, setRequest] = createSignal<ControlRequest | null>(makeRequest())
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
    const [request, setRequest] = createSignal<ControlRequest | null>(makeRequest())
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
})
