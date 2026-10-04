import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it, vi } from 'vitest'
import { isOhMyPiApproval, ohMyPiAskAnswer, ohMyPiCancelResponse, ohMyPiConfirmResponse, ohMyPiControlResponseSummary, ohMyPiValueResponse, sendOhMyPiResponse } from './controlResponse'

const approval = { type: 'extension_ui_request', id: 'a1', method: 'select', title: 'Allow tool: bash\nCommand: ls', options: ['Approve', 'Deny'] }

function saved(request: Record<string, unknown> | undefined, response: Record<string, unknown> | undefined): PersistedControlResponse {
  return { requestId: 'a1', claimToken: 't', request, response } as PersistedControlResponse
}

describe('ohMyPiValueResponse', () => {
  it('builds each of omp\'s answer shapes', () => {
    expect(ohMyPiValueResponse('a1', 'Approve')).toEqual({ type: 'extension_ui_response', id: 'a1', value: 'Approve' })
    expect(ohMyPiConfirmResponse('a1', false)).toEqual({ type: 'extension_ui_response', id: 'a1', confirmed: false })
    expect(ohMyPiCancelResponse('a1')).toEqual({ type: 'extension_ui_response', id: 'a1', cancelled: true })
  })
})

describe('ohMyPiAskAnswer', () => {
  it('builds the bridge\'s answer and leaves out empty halves', () => {
    expect(ohMyPiAskAnswer('q1', [
      { id: 'name', selected: [], custom: 'gamma' },
      { id: 'langs', selected: ['Go'], custom: '  ' },
    ])).toEqual({
      type: 'leapmux_ask_answer',
      id: 'q1',
      answers: [{ id: 'name', custom: 'gamma' }, { id: 'langs', selected: ['Go'] }],
    })
  })
})

describe('sendOhMyPiResponse', () => {
  it('sends the answer as JSON bytes', async () => {
    const onRespond = vi.fn(async () => {})
    await sendOhMyPiResponse(onRespond, ohMyPiValueResponse('a1', 'Deny'))
    const bytes = (onRespond.mock.calls[0] as unknown[] | undefined)?.[0] as Uint8Array
    expect(JSON.parse(new TextDecoder().decode(bytes))).toEqual({ type: 'extension_ui_response', id: 'a1', value: 'Deny' })
  })
})

describe('isOhMyPiApproval', () => {
  it('holds for the approval dialog alone', () => {
    expect(isOhMyPiApproval(approval)).toBe(true)
    expect(isOhMyPiApproval({ ...approval, options: ['Approve'] })).toBe(false)
    expect(isOhMyPiApproval({ ...approval, title: 'Pick one' })).toBe(false)
    expect(isOhMyPiApproval({ ...approval, method: 'confirm' })).toBe(false)
    expect(isOhMyPiApproval(undefined)).toBe(false)
  })

  it('holds for neither another frame type, a dialog without Approve, nor options that are not a list', () => {
    expect(isOhMyPiApproval({ ...approval, type: 'extension_ui_response' })).toBe(false)
    expect(isOhMyPiApproval({ ...approval, options: ['Deny'] })).toBe(false)
    expect(isOhMyPiApproval({ ...approval, options: 'Approve Deny' })).toBe(false)
    expect(isOhMyPiApproval({ ...approval, title: undefined })).toBe(false)
  })

  it('holds for an approval whose title is the prefix alone, or that offers more options', () => {
    expect(isOhMyPiApproval({ ...approval, title: 'Allow tool: ' })).toBe(true)
    expect(isOhMyPiApproval({ ...approval, options: ['Deny', 'Later', 'Approve'] })).toBe(true)
  })
})

describe('ohMyPiControlResponseSummary', () => {
  it('reads an approval decision', () => {
    expect(ohMyPiControlResponseSummary(saved(approval, ohMyPiValueResponse('a1', 'Approve')))).toEqual({ kind: 'label', text: 'Allow' })
    expect(ohMyPiControlResponseSummary(saved(approval, ohMyPiValueResponse('a1', 'Deny')))).toEqual({ kind: 'label', text: 'Deny' })
    expect(ohMyPiControlResponseSummary(saved(approval, ohMyPiValueResponse('a1', 'Maybe')))).toBeNull()
  })

  it('reads a cancellation', () => {
    expect(ohMyPiControlResponseSummary(saved(approval, ohMyPiCancelResponse('a1')))).toEqual({ kind: 'label', text: 'Cancelled' })
  })

  it('reads the bridge\'s answers under their questions', () => {
    const request = { type: 'leapmux_ask', id: 'q1', questions: [{ id: 'name', question: 'Project name?' }, { id: 'langs', question: 'Languages?' }] }
    const response = ohMyPiAskAnswer('q1', [{ id: 'name', selected: [], custom: 'gamma' }, { id: 'langs', selected: ['Go', 'Rust'], custom: 'Zig' }])
    expect(ohMyPiControlResponseSummary(saved(request, response))).toEqual({ kind: 'label', text: 'Project name?: gamma\nLanguages?: Go, Rust; Zig' })
    expect(ohMyPiControlResponseSummary(saved(undefined, { type: 'leapmux_ask_answer', id: 'q1', answers: [] }))).toEqual({ kind: 'label', text: 'Answered' })
  })

  it('heads an answer with its id when the request states no such question, and states the bare answer with no id', () => {
    const request = { type: 'leapmux_ask', id: 'q1', questions: [{ id: 'name', question: 'Project name?' }] }
    const response = { type: 'leapmux_ask_answer', id: 'q1', answers: [{ id: 'color', selected: ['blue'] }, { selected: ['x'], custom: 'y' }] }
    expect(ohMyPiControlResponseSummary(saved(request, response))).toEqual({ kind: 'label', text: 'color: blue\nx; y' })
  })

  it('leaves out a question the reader left blank, and reads an answer with no words as answered', () => {
    // The bridge's answer states each question's id, the unanswered ones included
    // (`ohMyPiAskAnswer` drops only the empty halves).
    const request = { type: 'leapmux_ask', id: 'q1', questions: [{ id: 'name', question: 'Project name?' }, { id: 'langs', question: 'Languages?' }] }
    const partly = ohMyPiAskAnswer('q1', [{ id: 'name', selected: [], custom: '  ' }, { id: 'langs', selected: ['Go'], custom: '' }])
    expect(ohMyPiControlResponseSummary(saved(request, partly))).toEqual({ kind: 'label', text: 'Languages?: Go' })
    const blank = ohMyPiAskAnswer('q1', [{ id: 'name', selected: [], custom: '' }, { id: 'langs', selected: [], custom: '' }])
    expect(ohMyPiControlResponseSummary(saved(request, blank))).toEqual({ kind: 'label', text: 'Answered' })
  })

  it('reads the bridge\'s answers when the answer list is not a list or holds no records', () => {
    expect(ohMyPiControlResponseSummary(saved(undefined, { type: 'leapmux_ask_answer', id: 'q1', answers: 'gamma' }))).toEqual({ kind: 'label', text: 'Answered' })
    expect(ohMyPiControlResponseSummary(saved(undefined, { type: 'leapmux_ask_answer', id: 'q1', answers: ['gamma', null] }))).toEqual({ kind: 'label', text: 'Answered' })
  })

  it('reads a cancellation before any other field the response states', () => {
    // omp's dismissal carries `cancelled` alone; a response that also states a value
    // is still a dismissal.
    expect(ohMyPiControlResponseSummary(saved(approval, { ...ohMyPiValueResponse('a1', 'Approve'), cancelled: true }))).toEqual({ kind: 'label', text: 'Cancelled' })
    expect(ohMyPiControlResponseSummary(saved(undefined, { type: 'leapmux_ask_answer', id: 'q1', answers: [], cancelled: true }))).toEqual({ kind: 'label', text: 'Cancelled' })
  })

  it('reads a response with `cancelled: false` by its other fields', () => {
    const input = { type: 'extension_ui_request', id: 'i1', method: 'input', title: 'Name' }
    expect(ohMyPiControlResponseSummary(saved(input, { ...ohMyPiValueResponse('i1', 'main'), cancelled: false }))).toEqual({ kind: 'label', text: 'main' })
  })

  it('reads a confirm and a value', () => {
    const confirm = { type: 'extension_ui_request', id: 'c1', method: 'confirm', title: 'Proceed?' }
    expect(ohMyPiControlResponseSummary(saved(confirm, ohMyPiConfirmResponse('c1', true)))).toEqual({ kind: 'label', text: 'Confirmed' })
    expect(ohMyPiControlResponseSummary(saved(confirm, ohMyPiConfirmResponse('c1', false)))).toEqual({ kind: 'label', text: 'Declined' })
    const input = { type: 'extension_ui_request', id: 'i1', method: 'input', title: 'Name' }
    expect(ohMyPiControlResponseSummary(saved(input, ohMyPiValueResponse('i1', 'main')))).toEqual({ kind: 'label', text: 'main' })
    expect(ohMyPiControlResponseSummary(saved(input, ohMyPiValueResponse('i1', '')))).toEqual({ kind: 'label', text: 'Empty answer' })
  })

  it('answers null for a response it cannot read', () => {
    expect(ohMyPiControlResponseSummary(saved(approval, undefined))).toBeNull()
    expect(ohMyPiControlResponseSummary(saved(undefined, { type: 'something' }))).toBeNull()
    // A value or a confirmation of the wrong type states no answer.
    expect(ohMyPiControlResponseSummary(saved(undefined, { type: 'extension_ui_response', id: 'i1', value: 7 }))).toBeNull()
    expect(ohMyPiControlResponseSummary(saved(undefined, { type: 'extension_ui_response', id: 'c1', confirmed: 'yes' }))).toBeNull()
    // An approval answered with a confirmation is no decision the approval offers.
    expect(ohMyPiControlResponseSummary(saved(approval, ohMyPiConfirmResponse('a1', true)))).toBeNull()
  })
})
