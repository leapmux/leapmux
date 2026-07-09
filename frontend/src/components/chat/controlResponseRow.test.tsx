import type { ControlResponseDisplay, PersistedControlResponse } from './persistedControlResponse'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { renderControlResponseRow, renderMessageContent } from './messageRenderers'
// Register provider plugins so renderMessageContent can resolve a plugin's controlResponseDisplay.
import '~/components/chat/providers'

function row(parsed: unknown, display?: (cr: PersistedControlResponse) => ControlResponseDisplay | null) {
  return render(() => <>{renderControlResponseRow(parsed, undefined, display)}</>)
}

const ENVELOPE = { isSynthetic: true, controlResponse: { provider: 'CODEX', response: {} } }

describe('rendercontrolresponserow', () => {
  it('renders a label as line-broken plain text', () => {
    const { container } = row(ENVELOPE, () => ({ kind: 'label', text: 'Task: Build\nEnv: Dev' }))
    expect(container.textContent).toBe('Task: Build\nEnv: Dev')
  })

  it('renders feedback under the "Sent feedback:" lead as markdown', () => {
    const { container } = row(ENVELOPE, () => ({ kind: 'feedback', message: 'use ripgrep instead' }))
    expect(container.textContent).toContain('Sent feedback:')
    expect(container.textContent).toContain('use ripgrep instead')
  })

  it('degrades to the neutral/generic fallback when the deriver returns null', () => {
    // No plugin display + an unrecognized response -> the generic terminal label.
    const { container } = row(ENVELOPE, () => null)
    expect(container.textContent).toBe('Responded')
  })

  it('degrades to the fallback when the deriver THROWS, never leaking raw JSON', () => {
    // A derivation that throws on a malformed payload must NOT propagate to renderMessageContent's
    // raw-JSON safety net (which would dump the {controlResponse:...} envelope at the user) -- it
    // degrades to the same neutral fallback as a null return.
    const throwing = (): never => {
      throw new Error('bad payload')
    }
    const { container } = row(ENVELOPE, throwing)
    expect(container.textContent).toBe('Responded')
  })

  it('uses the coarse behavior envelope as the fallback when no deriver is given', () => {
    const parsed = { isSynthetic: true, controlResponse: { provider: 'CLAUDE_CODE', response: { response: { response: { behavior: 'allow' } } } } }
    const { container } = row(parsed, undefined)
    expect(container.textContent).toBe('Approved')
  })

  it('returns null (no row) for a non-control-response object', () => {
    const { container } = row({ content: 'hello' }, () => ({ kind: 'label', text: 'x' }))
    expect(container.textContent).toBe('')
  })
})

describe('rendermessagecontent control_response dispatch', () => {
  // The transcript-side counterpart to chatMarkPreview's rail-side end-to-end test: a
  // control_response category dispatches through renderMessageContent's shared branch to the
  // resolved plugin's controlResponseDisplay -- no per-plugin renderMessage case needed.
  function renderRow(parsed: unknown, provider: AgentProvider) {
    return render(() => <>{renderMessageContent(parsed, undefined, { kind: 'control_response' }, provider)}</>)
  }

  it('renders a Codex decision label through the codex plugin', () => {
    const parsed = {
      isSynthetic: true,
      controlResponse: {
        provider: 'CODEX',
        request: { method: 'item/commandExecution/requestApproval' },
        response: { result: { decision: 'accept' } },
      },
    }
    expect(renderRow(parsed, AgentProvider.CODEX).container.textContent).toBe('Allow')
  })

  it('renders a Claude deny-with-feedback through the claude plugin', () => {
    const parsed = {
      isSynthetic: true,
      controlResponse: {
        provider: 'CLAUDE_CODE',
        response: { type: 'control_response', response: { request_id: 'r', response: { behavior: 'deny', message: 'add tests' } } },
      },
    }
    const text = renderRow(parsed, AgentProvider.CLAUDE_CODE).container.textContent ?? ''
    expect(text).toContain('Sent feedback:')
    expect(text).toContain('add tests')
  })

  it('degrades an unrecognized payload to the generic label', () => {
    const parsed = { isSynthetic: true, controlResponse: { provider: 'CODEX', response: {} } }
    expect(renderRow(parsed, AgentProvider.CODEX).container.textContent).toBe('Responded')
  })
})
