import type { ControlPrompt } from '../../model/controlPrompt'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { resolveControlResponseSummary } from '../../persistedControlResponse'
import { piControlResponseSummary } from './controlResponse'

/**
 * The prompt that the Worker reads from a Pi dialog of one method
 * (`ReadControlRequest` in the Go `pi` package): an `input` or a `select` is a
 * question, and every other method is a dialog, a confirm unless it is an `editor`.
 */
function promptOf(method: string): ControlPrompt {
  switch (method) {
    case 'select':
      return {
        kind: 'question',
        questions: [
          { question: 'Choose an option', options: [] },
        ],
      }
    case 'input':
      return {
        kind: 'question',
        questions: [
          { question: 'Enter a value', options: [], allowEmpty: true },
        ],
      }
    case 'editor':
      return {
        kind: 'dialog',
        dialog: { title: 'Approval Required', variant: 'editor' },
      }
    default:
      return {
        kind: 'dialog',
        dialog: { title: 'Approval Required', variant: 'confirm' },
      }
  }
}

/** The prompt of Pi's plan menu, whose extension choice is an option beyond the three plan actions. */
const PLAN_PROMPT: ControlPrompt = {
  kind: 'plan',
  choices: [
    { id: 'Export plan…', label: 'Export plan…', effect: 'other' },
  ],
}

const PLAN_REQUEST = {
  type: 'extension_ui_request',
  method: 'select',
  title: 'Proposed plan ready. What next?',
  options: [
    'Implement here',
    'Start fresh and implement',
    'Stay in Plan mode',
    'Export plan…',
  ],
}

/** One saved answer to a dialog of `method`, with the prompt that the Worker records for it. */
function cr(method: string, response: Record<string, unknown> | undefined, prompt: ControlPrompt | null = promptOf(method)): PersistedControlResponse {
  return {
    claimToken: 'claim-1',
    requestId: 'r',
    request: { type: 'extension_ui_request', method },
    response,
    ...(prompt
      ? { prompt }
      : {}),
  }
}

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
    expect(piControlResponseSummary(cr('confirm', { value: 'yes' }))).toBeNull()
    expect(piControlResponseSummary(cr('confirm', { confirmed: true }))).toEqual({ kind: 'label', text: 'Approved' })
    expect(piControlResponseSummary(cr('confirm', { confirmed: false }))).toEqual({ kind: 'label', text: 'Rejected' })
  })

  it.each([
    ['Implement here', 'Approved'],
    ['Start fresh and implement', 'Approved'],
    ['Stay in Plan mode', 'Rejected'],
    ['Export plan…', 'Export plan…'],
  ])('renders the plan decision %s as %s', (value, expected) => {
    expect(piControlResponseSummary({
      ...cr('select', { value }, PLAN_PROMPT),
      request: PLAN_REQUEST,
    }))
      .toEqual({ kind: 'label', text: expected })
  })

  it('reads nothing from a plan answer that states no value', () => {
    expect(piControlResponseSummary(cr('select', { confirmed: true }, PLAN_PROMPT))).toBeNull()
  })

  // The prompt states which control the reader answered, so the plan reads as a plan
  // by its prompt alone, and a select whose title merely resembles the menu reads as a
  // question.
  it('reads the plan by the recorded prompt, not by the native request', () => {
    expect(piControlResponseSummary(cr('select', { value: 'Implement here' }, PLAN_PROMPT))).toEqual({ kind: 'label', text: 'Approved' })
    expect(piControlResponseSummary({
      ...cr('select', { value: 'Stay in Plan mode' }),
      request: PLAN_REQUEST,
    })).toEqual({ kind: 'label', text: 'Stay in Plan mode' })
  })

  it('labels a cancellation regardless of the control', () => {
    expect(piControlResponseSummary(cr('confirm', { cancelled: true }))).toEqual({ kind: 'label', text: 'Cancelled' })
    expect(piControlResponseSummary(cr('select', { cancelled: true }, PLAN_PROMPT))).toEqual({ kind: 'label', text: 'Cancelled' })
    expect(piControlResponseSummary(cr('input', { cancelled: true }, null))).toEqual({ kind: 'label', text: 'Cancelled' })
  })

  it('shows the typed value for select / input / editor dialogs', () => {
    expect(piControlResponseSummary(cr('select', { value: '  Blue  ' }))).toEqual({ kind: 'label', text: '  Blue  ' })
    expect(piControlResponseSummary(cr('input', { value: 'note' }))).toEqual({ kind: 'label', text: 'note' })
    expect(piControlResponseSummary(cr('editor', { value: 'body' }))).toEqual({ kind: 'label', text: 'body' })
  })

  it('preserves a select value that resembles an obsolete MCP approval choice', () => {
    expect(piControlResponseSummary({
      ...cr('select', { value: 'Allow once' }),
      request: {
        type: 'extension_ui_request',
        method: 'select',
        title: 'MCP: probe wants to run lookup\n\nArguments:\n{}',
        options: ['Allow once', 'Allow for session', 'Deny'],
      },
    })).toEqual({ kind: 'label', text: 'Allow once' })
  })

  // The Worker reads a method it does not know as a confirm, so the user can always
  // answer it, and its answer is a confirmation.
  it('reads the answer to a method the Worker does not know as a confirmation', () => {
    expect(piControlResponseSummary(cr('mystery', { confirmed: true }))).toEqual({ kind: 'label', text: 'Approved' })
    expect(piControlResponseSummary(cr('mystery', { value: 'x' }))).toBeNull()
    expect(piControlResponseSummary(cr('confirm', undefined))).toBeNull()
  })

  // The prompt lives in the answer row's own metadata, so a row whose request is gone
  // still reads its control.
  it('reads the answer of a dialog whose native request is gone', () => {
    const gone = (response: Record<string, unknown>, prompt: ControlPrompt): PersistedControlResponse => ({
      claimToken: 'claim-1',
      requestId: 'r',
      request: undefined,
      response,
      prompt,
    })
    expect(piControlResponseSummary(gone({ confirmed: true }, promptOf('confirm')))).toEqual({ kind: 'label', text: 'Approved' })
    expect(piControlResponseSummary(gone({ confirmed: false }, promptOf('confirm')))).toEqual({ kind: 'label', text: 'Rejected' })
    expect(piControlResponseSummary(gone({ value: 'note' }, promptOf('input')))).toEqual({ kind: 'label', text: 'note' })
    expect(piControlResponseSummary(gone({ value: 'Implement here' }, PLAN_PROMPT))).toEqual({ kind: 'label', text: 'Approved' })
    expect(piControlResponseSummary(gone({}, promptOf('input')))).toBeNull()
  })

  // The prompt states which control the answer belongs to. A row with none reads as
  // nothing beyond a cancellation, and the shared chokepoint shows its generic label.
  it('reads nothing but a cancellation from a row that records no prompt', () => {
    const row = cr('confirm', { confirmed: true }, null)
    expect(piControlResponseSummary(row)).toBeNull()
    expect(piControlResponseSummary(cr('input', { value: 'note' }, null))).toBeNull()
    expect(resolveControlResponseSummary(row, piControlResponseSummary)).toEqual({ kind: 'label', text: 'Responded' })
  })

  it('reads nothing for a prompt that no Pi dialog states', () => {
    expect(piControlResponseSummary(cr('select', { value: 'Allow' }, {
      kind: 'permission',
      permission: { options: [] },
    }))).toBeNull()
  })
})
