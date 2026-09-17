import type { ControlResponseDisplay, PersistedControlResponse } from './persistedControlResponse'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { renderControlResponseRow } from './messageRenderers'
import { resolveControlResponseDisplay } from './persistedControlResponse'
import { renderMessageContent } from './rowRenderers'
// Register provider plugins so renderMessageContent can resolve a plugin's controlResponseDisplay.
import '~/components/chat/providers'

// The renderer takes the DISPLAY: layer 1 runs the provider's derivation and the
// never-null chokepoint (~/components/chat/rowExtraction.ts), so the markup below is
// all this function decides. The derivation and its three degradations are asserted
// against `resolveControlResponseDisplay` itself, in the describe after this one.
function row(display: ControlResponseDisplay) {
  return render(() => <>{renderControlResponseRow(display, undefined)}</>)
}

const RESPONSE: PersistedControlResponse = { requestId: 'request-1', claimToken: 'claim-1', request: undefined, response: {} }

describe('renderControlResponseRow', () => {
  it('renders a label as line-broken plain text', () => {
    const { container } = row({ kind: 'label', text: 'Task: Build\nEnv: Dev' })
    expect(container.textContent).toBe('Task: Build\nEnv: Dev')
  })

  it('renders feedback under the "Sent feedback:" lead as markdown', () => {
    const { container } = row({ kind: 'feedback', message: 'use ripgrep instead' })
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

describe('resolveControlResponseDisplay', () => {
  it('degrades to the neutral/generic fallback when the deriver returns null', () => {
    // No plugin display + an unrecognized response -> the generic label.
    expect(resolveControlResponseDisplay(RESPONSE, () => null)).toEqual({ kind: 'label', text: 'Responded' })
  })

  it('degrades to the fallback when the deriver THROWS, never leaking raw JSON', () => {
    // A derivation that throws on a malformed payload must NOT propagate to the
    // extraction's own guard, which would report the row as one LeapMux could not
    // render -- it degrades to the same neutral fallback as a null return.
    const throwing = (): never => {
      throw new Error('bad payload')
    }
    expect(resolveControlResponseDisplay(RESPONSE, throwing)).toEqual({ kind: 'label', text: 'Responded' })
  })

  it('uses the coarse behavior envelope as the fallback when no deriver is given', () => {
    const parsed = { ...RESPONSE, response: { response: { response: { behavior: 'allow' } } } }
    expect(resolveControlResponseDisplay(parsed, undefined)).toEqual({ kind: 'label', text: 'Allow' })
  })
})

describe('renderMessageContent control_response dispatch', () => {
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
