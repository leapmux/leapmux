import type { ControlPrompt } from './model/controlPrompt'
import type { ControlResponseSummary } from './model/controlResponse'
import type { PersistedControlResponse } from './persistedControlResponse'
import type { ControlDecision } from '~/generated/proto/leapmux/v1/agent_control_pb'
import { create } from '@bufbuild/protobuf'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { ControlDecisionSchema } from '~/generated/proto/leapmux/v1/agent_control_pb'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { settleWorkerReplies } from '~/test-support/fakeWorker'
import { renderMessageContent } from '~/test-support/messageContentRenderer'
import { renderControlResponseRow } from './messageRenderers'
// Register provider plugins, as the transcript does, so a provider's own reader could
// claim the row if the shared display did not.
import '~/components/chat/providers'

// The renderer takes the DISPLAY: layer 1 resolves it from the prompt and the decision
// that the Worker records (~/components/chat/rowExtraction.ts), so the markup below is
// all this function decides. The display itself is asserted in
// controlDecisionSummary.test.ts and persistedControlResponse.test.ts.
function row(display: ControlResponseSummary) {
  return render(() => <>{renderControlResponseRow(display, undefined)}</>)
}

const RESPONSE: PersistedControlResponse = {
  requestId: 'request-1',
  claimToken: 'claim-1',
  request: undefined,
  response: {},
}

describe('renderControlResponseRow', () => {
  it('renders a label as line-broken plain text', () => {
    const { container } = row({ kind: 'label', text: 'Task: Build\nEnv: Dev' })
    expect(container.textContent).toBe('Task: Build\nEnv: Dev')
  })

  it('renders feedback under the "Sent feedback:" lead as markdown', async () => {
    const { container } = row({ kind: 'feedback', message: 'use ripgrep instead' })
    await settleWorkerReplies()
    expect(container.textContent).toContain('Sent feedback:')
    expect(container.textContent).toContain('use ripgrep instead')
  })

  // An empty label is a row the reader cannot read anything out of, and the chokepoint
  // above is what keeps one from arriving -- this states that the markup itself makes
  // no attempt to repair it, so the guard stays where every caller passes through it.
  it('draws the label it is given, with no repair of its own', () => {
    const { container } = row({ kind: 'label', text: '' })
    expect(container.querySelector('[data-testid="control-response-text"]')?.textContent).toBe('')
  })
})

describe('renderMessageContent control_response dispatch', () => {
  function renderRow(response: PersistedControlResponse, provider: AgentProvider) {
    return render(() => <>{renderMessageContent(response.response, undefined, { kind: 'control_response', response }, provider)}</>)
  }

  const permission: ControlPrompt = {
    kind: 'permission',
    permission: {
      title: 'Run',
      options: [
        { optionId: 'accept', kind: 'allow_once', name: 'Allow' },
        { optionId: 'deny', kind: 'reject_once', name: 'Deny' },
      ],
    },
  }

  function decided(optionId: string, feedback = ''): ControlDecision {
    return create(ControlDecisionSchema, {
      kind: {
        case: 'permission',
        value: { optionId, feedback },
      },
    })
  }

  it('renders the words of the decision, not of the provider\'s answer', () => {
    const response = {
      ...RESPONSE,
      response: {
        result: { decision: 'accept' },
      },
      prompt: permission,
      decision: decided('accept'),
    }
    expect(renderRow(response, AgentProvider.CODEX).container.textContent).toBe('Allow')
  })

  it('renders the feedback of a refusal under its lead', async () => {
    const response = {
      ...RESPONSE,
      prompt: permission,
      decision: decided('deny', 'add tests'),
    }
    const { container } = renderRow(response, AgentProvider.CLAUDE_CODE)
    await settleWorkerReplies()
    const text = container.textContent ?? ''
    expect(text).toContain('Sent feedback:')
    expect(text).toContain('add tests')
  })

  it('uses the generic label for malformed original response content', () => {
    const response = { ...RESPONSE, response: undefined }
    const rendered = render(() => <>{renderMessageContent('{unfinished', undefined, { kind: 'control_response', response }, AgentProvider.CODEX)}</>)
    expect(rendered.container.textContent).toBe('Responded')
  })
})
