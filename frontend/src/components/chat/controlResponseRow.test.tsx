import type { ControlResponseDisplay, PersistedControlResponse } from './persistedControlResponse'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { renderControlResponseRow, renderMessageContent } from './messageRenderers'
// Register provider plugins so renderMessageContent can resolve a plugin's controlResponseDisplay.
import '~/components/chat/providers'

function row(parsed: PersistedControlResponse, display?: (cr: PersistedControlResponse) => ControlResponseDisplay | null) {
  return render(() => <>{renderControlResponseRow(parsed, undefined, display)}</>)
}

const RESPONSE: PersistedControlResponse = { requestId: 'request-1', claimToken: 'claim-1', request: undefined, response: {} }

describe('rendercontrolresponserow', () => {
  it('renders a label as line-broken plain text', () => {
    const { container } = row(RESPONSE, () => ({ kind: 'label', text: 'Task: Build\nEnv: Dev' }))
    expect(container.textContent).toBe('Task: Build\nEnv: Dev')
  })

  it('renders feedback under the "Sent feedback:" lead as markdown', () => {
    const { container } = row(RESPONSE, () => ({ kind: 'feedback', message: 'use ripgrep instead' }))
    expect(container.textContent).toContain('Sent feedback:')
    expect(container.textContent).toContain('use ripgrep instead')
  })

  it('degrades to the neutral/generic fallback when the deriver returns null', () => {
    // No plugin display + an unrecognized response -> the generic label.
    const { container } = row(RESPONSE, () => null)
    expect(container.textContent).toBe('Responded')
  })

  it('degrades to the fallback when the deriver THROWS, never leaking raw JSON', () => {
    // A derivation that throws on a malformed payload must NOT propagate to renderMessageContent's
    // raw-JSON safety net (which would dump the {controlResponse:...} envelope at the user) -- it
    // degrades to the same neutral fallback as a null return.
    const throwing = (): never => {
      throw new Error('bad payload')
    }
    const { container } = row(RESPONSE, throwing)
    expect(container.textContent).toBe('Responded')
  })

  it('uses the coarse behavior envelope as the fallback when no deriver is given', () => {
    const parsed = { ...RESPONSE, response: { response: { response: { behavior: 'allow' } } } }
    const { container } = row(parsed, undefined)
    expect(container.textContent).toBe('Allow')
  })
})

describe('rendermessagecontent control_response dispatch', () => {
  function renderRow(response: PersistedControlResponse, provider: AgentProvider) {
    return render(() => <>{renderMessageContent(response.response, undefined, { kind: 'control_response', response }, provider)}</>)
  }

  it('renders a Codex decision through the provider plugin', () => {
    const response = { ...RESPONSE, request: { method: 'item/commandExecution/requestApproval' }, response: { result: { decision: 'accept' } } }
    expect(renderRow(response, AgentProvider.CODEX).container.textContent).toBe('Allow')
  })

  it('renders Claude rejection feedback through the provider plugin', () => {
    const response = { ...RESPONSE, response: { type: 'control_response', response: { request_id: 'r', response: { behavior: 'deny', message: 'add tests' } } } }
    const text = renderRow(response, AgentProvider.CLAUDE_CODE).container.textContent ?? ''
    expect(text).toContain('Sent feedback:')
    expect(text).toContain('add tests')
  })

  it('uses the generic label for malformed original response content', () => {
    const response = { ...RESPONSE, response: undefined }
    const rendered = render(() => <>{renderMessageContent('{unfinished', undefined, { kind: 'control_response', response }, AgentProvider.CODEX)}</>)
    expect(rendered.container.textContent).toBe('Responded')
  })
})
