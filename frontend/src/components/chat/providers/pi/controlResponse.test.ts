import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { createControlAnswerState } from '../../controls/types'
import {
  piAskAnswerValue,
  piCancelResponse,
  piConfirmResponse,
  piControlResponseSummary,
  piValueResponse,
  sendPiExtensionResponse,
} from './controlResponse'

function cr(method: string, response: Record<string, unknown> | undefined): PersistedControlResponse {
  return { claimToken: 'claim-1', requestId: 'r', request: { method }, response }
}

describe('pi controlResponse helpers', () => {
  it('preserves saved multi-select choices when source details are unavailable', () => {
    const state = createControlAnswerState({ selections: { 0: ['1', '2'] } })
    expect(piAskAnswerValue(state, [{ question: 'Choose', options: [] }], { method: 'input', placeholder: '1,3' })).toBe('1,2')
  })

  it('preserves numeric-looking custom text as typed', () => {
    const state = createControlAnswerState({ customTexts: { 0: '1. A — custom text' } })
    expect(piAskAnswerValue(state, [], { method: 'input', placeholder: '1,3' })).toBe('1. A — custom text')
  })

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
    await sendPiExtensionResponse(async (content) => {
      captured = content
    }, piValueResponse('req-1', 'Allow'))

    expect(captured).not.toBeNull()
    const text = new TextDecoder().decode(captured!)
    const parsed = JSON.parse(text)
    expect(parsed).toEqual({ type: 'extension_ui_response', id: 'req-1', value: 'Allow' })
  })
})

describe('piControlResponseSummary', () => {
  it('preserves native text whitespace and distinguishes an explicit empty answer', () => {
    for (const method of ['select', 'input', 'editor']) {
      for (const value of ['  first line\n\tsecond line\n  ', '   '])
        expect(piControlResponseSummary(cr(method, { value }))).toEqual({ kind: 'label', text: value })
      expect(piControlResponseSummary(cr(method, { value: '' }))).toEqual({ kind: 'label', text: 'Empty answer' })
      expect(piControlResponseSummary(cr(method, {}))).toBeNull()
    }
  })

  it('requires an explicit confirmation and uses shared decision labels', () => {
    expect(piControlResponseSummary(cr('confirm', {}))).toBeNull()
    expect(piControlResponseSummary(cr('confirm', { confirmed: true }))).toEqual({ kind: 'label', text: 'Approved' })
    expect(piControlResponseSummary(cr('confirm', { confirmed: false }))).toEqual({ kind: 'label', text: 'Rejected' })
  })
  it.each([
    ['Implement here', 'Approved'],
    ['Start fresh and implement', 'Approved'],
    ['Stay in Plan mode', 'Rejected'],
    ['Export plan…', 'Export plan…'],
  ])('renders the plan decision %s as %s', (value, expected) => {
    expect(piControlResponseSummary({ ...cr('select', { value }), request: {
      type: 'extension_ui_request',
      method: 'select',
      title: 'Proposed plan ready. What next?',
      options: ['Implement here', 'Start fresh and implement', 'Stay in Plan mode'],
    } }))
      .toEqual({ kind: 'label', text: expected })
  })

  it('labels a cancellation regardless of method', () => {
    expect(piControlResponseSummary(cr('confirm', { cancelled: true }))).toEqual({ kind: 'label', text: 'Cancelled' })
  })

  it('shows the confirmed decision', () => {
    expect(piControlResponseSummary(cr('confirm', { confirmed: true }))).toEqual({ kind: 'label', text: 'Approved' })
    expect(piControlResponseSummary(cr('confirm', { confirmed: false }))).toEqual({ kind: 'label', text: 'Rejected' })
  })

  it('shows the typed value for select / input / editor dialogs', () => {
    expect(piControlResponseSummary(cr('select', { value: '  Blue  ' }))).toEqual({ kind: 'label', text: '  Blue  ' })
    expect(piControlResponseSummary(cr('input', { value: 'note' }))).toEqual({ kind: 'label', text: 'note' })
    expect(piControlResponseSummary(cr('editor', { value: 'body' }))).toEqual({ kind: 'label', text: 'body' })
  })

  it('preserves whitespace and leaves unknown methods unresolved', () => {
    expect(piControlResponseSummary(cr('select', { value: '   ' }))).toEqual({ kind: 'label', text: '   ' })
    expect(piControlResponseSummary(cr('mystery', { value: 'x' }))).toBeNull()
    expect(piControlResponseSummary(cr('confirm', undefined))).toBeNull()
  })

  it('recovers the dialog from the response shape when the request is gone', () => {
    // A native response can supply its value even when the matching request is unavailable.
    const gone = (response: Record<string, unknown>): PersistedControlResponse => ({ claimToken: 'claim-1', requestId: 'r', request: undefined, response })
    expect(piControlResponseSummary(gone({ confirmed: true }))).toEqual({ kind: 'label', text: 'Approved' })
    expect(piControlResponseSummary(gone({ confirmed: false }))).toEqual({ kind: 'label', text: 'Rejected' })
    expect(piControlResponseSummary(gone({ value: 'note' }))).toEqual({ kind: 'label', text: 'note' })
    expect(piControlResponseSummary(gone({ cancelled: true }))).toEqual({ kind: 'label', text: 'Cancelled' })
    expect(piControlResponseSummary(gone({}))).toBeNull()
  })
})
