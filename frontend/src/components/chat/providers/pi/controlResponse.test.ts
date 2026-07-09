import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import {
  piCancelResponse,
  piConfirmResponse,
  piControlResponseDisplay,
  piValueResponse,
  sendPiExtensionResponse,
} from './controlResponse'

function cr(method: string, response: Record<string, unknown> | undefined): PersistedControlResponse {
  return { provider: 'PI', requestId: 'r', request: { method }, response }
}

describe('pi controlResponse helpers', () => {
  it('builds value responses for select / input / editor', () => {
    expect(piValueResponse('req-1', 'Allow')).toEqual({
      type: 'extension_ui_response',
      id: 'req-1',
      value: 'Allow',
    })
  })

  it('builds confirm responses with confirmed=true', () => {
    expect(piConfirmResponse('req-2', true)).toEqual({
      type: 'extension_ui_response',
      id: 'req-2',
      confirmed: true,
    })
  })

  it('builds confirm responses with confirmed=false', () => {
    expect(piConfirmResponse('req-2', false)).toEqual({
      type: 'extension_ui_response',
      id: 'req-2',
      confirmed: false,
    })
  })

  it('builds cancellation responses', () => {
    expect(piCancelResponse('req-3')).toEqual({
      type: 'extension_ui_response',
      id: 'req-3',
      cancelled: true,
    })
  })

  it('serializes responses through onRespond as UTF-8 JSON', async () => {
    let captured: Uint8Array | null = null
    await sendPiExtensionResponse('agent', async (_id, content) => {
      captured = content
    }, piValueResponse('req-1', 'Allow'))

    expect(captured).not.toBeNull()
    const text = new TextDecoder().decode(captured!)
    const parsed = JSON.parse(text)
    expect(parsed).toEqual({ type: 'extension_ui_response', id: 'req-1', value: 'Allow' })
  })
})

describe('picontrolresponsedisplay', () => {
  it('labels a cancellation regardless of method', () => {
    expect(piControlResponseDisplay(cr('confirm', { cancelled: true }))).toEqual({ kind: 'label', text: 'Cancelled' })
  })

  it('maps a confirm dialog to Approve / Deny', () => {
    expect(piControlResponseDisplay(cr('confirm', { confirmed: true }))).toEqual({ kind: 'label', text: 'Approve' })
    expect(piControlResponseDisplay(cr('confirm', { confirmed: false }))).toEqual({ kind: 'label', text: 'Deny' })
  })

  it('shows the typed value for select / input / editor dialogs', () => {
    expect(piControlResponseDisplay(cr('select', { value: '  Blue  ' }))).toEqual({ kind: 'label', text: 'Blue' })
    expect(piControlResponseDisplay(cr('input', { value: 'note' }))).toEqual({ kind: 'label', text: 'note' })
    expect(piControlResponseDisplay(cr('editor', { value: 'body' }))).toEqual({ kind: 'label', text: 'body' })
  })

  it('returns null for an empty value or an unknown method (caller degrades)', () => {
    expect(piControlResponseDisplay(cr('select', { value: '   ' }))).toBeNull()
    expect(piControlResponseDisplay(cr('mystery', { value: 'x' }))).toBeNull()
    expect(piControlResponseDisplay(cr('confirm', undefined))).toBeNull()
  })

  it('recovers the dialog from the response shape when the request is gone', () => {
    // Request-gone: the pruned request (and its method) is absent, but the response shape still
    // identifies the dialog -- a `confirmed` boolean is a confirm, a non-empty `value` is a text
    // dialog -- so the answer renders instead of degrading to the generic label.
    const gone = (response: Record<string, unknown>): PersistedControlResponse => ({ provider: 'PI', requestId: 'r', request: undefined, response })
    expect(piControlResponseDisplay(gone({ confirmed: true }))).toEqual({ kind: 'label', text: 'Approve' })
    expect(piControlResponseDisplay(gone({ confirmed: false }))).toEqual({ kind: 'label', text: 'Deny' })
    expect(piControlResponseDisplay(gone({ value: 'note' }))).toEqual({ kind: 'label', text: 'note' })
    expect(piControlResponseDisplay(gone({ cancelled: true }))).toEqual({ kind: 'label', text: 'Cancelled' })
    expect(piControlResponseDisplay(gone({}))).toBeNull()
  })
})
