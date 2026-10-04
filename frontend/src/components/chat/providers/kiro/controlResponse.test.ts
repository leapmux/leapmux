import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { kiroControlResponseSummary, kiroPermissionRejectReason } from './controlResponse'

function record(request: Record<string, unknown> | undefined, result: Record<string, unknown> | undefined): PersistedControlResponse {
  return { requestId: 'jsonrpc:1', claimToken: 'claim', request, response: result === undefined ? undefined : { jsonrpc: '2.0', id: 1, result } }
}

const QUESTION = { jsonrpc: '2.0', id: 1, method: '_kiro/userInput', params: { question: 'Which DB?', options: [{ title: 'Postgres' }] } }
const ELICIT = { jsonrpc: '2.0', id: 1, method: '_kiro/mcp/elicitation', params: { elicitation: { mode: 'form', message: 'Choose', requestedSchema: { type: 'object', properties: { size: { type: 'string', title: 'Size' } } } } } }
const PERMISSION = { jsonrpc: '2.0', id: 1, method: 'session/request_permission', params: {
  toolCall: { toolCallId: 'c' },
  options: [
    { optionId: 'accept', name: 'Allow', kind: 'allow_once' },
    { optionId: 'always-accept', name: 'Always allow', kind: 'allow_always' },
    { optionId: 'reject', name: 'Deny', kind: 'reject_once' },
  ],
} }

describe('kiroPermissionRejectReason', () => {
  it('adds the reason under Kiro\'s metadata and keeps the rest', () => {
    const result = { outcome: { outcome: 'selected', optionId: 'reject' }, _meta: { other: 1, kiro: { editedCommand: 'ls' } } }
    expect(kiroPermissionRejectReason(result, 'Use rg')).toEqual({
      outcome: { outcome: 'selected', optionId: 'reject' },
      _meta: { other: 1, kiro: { editedCommand: 'ls', rejectionReason: 'Use rg' } },
    })
  })

  it('adds a meta object to a result that has none', () => {
    expect(kiroPermissionRejectReason({ outcome: { outcome: 'cancelled' } }, 'why')).toEqual({ outcome: { outcome: 'cancelled' }, _meta: { kiro: { rejectionReason: 'why' } } })
  })

  it('replaces a meta or a kiro value that is no object', () => {
    expect(kiroPermissionRejectReason({ _meta: 'x' }, 'why')).toEqual({ _meta: { kiro: { rejectionReason: 'why' } } })
    expect(kiroPermissionRejectReason({ _meta: { other: 1, kiro: 'x' } }, 'why')).toEqual({ _meta: { other: 1, kiro: { rejectionReason: 'why' } } })
  })

  it('leaves the result it received unchanged', () => {
    const result = { _meta: { kiro: { editedCommand: 'ls' } } }
    kiroPermissionRejectReason(result, 'why')
    expect(result).toEqual({ _meta: { kiro: { editedCommand: 'ls' } } })
  })
})

describe('kiroControlResponseSummary', () => {
  it('shows the answer of a question, and a dismissal', () => {
    expect(kiroControlResponseSummary(record(QUESTION, { action: 'answered', answer: 'Postgres [PostGIS]' }))).toEqual({ kind: 'label', text: 'Postgres [PostGIS]' })
    expect(kiroControlResponseSummary(record(QUESTION, { action: 'dismissed' }))).toEqual({ kind: 'label', text: 'Dismissed' })
    expect(kiroControlResponseSummary(record(QUESTION, { action: 'other' }))).toBeNull()
  })

  it('shows an MCP form answer through the shared form display', () => {
    // The registration wraps the shared form display around this one. Alone, a form
    // answer takes the permission display, which reads no option and answers null.
    expect(kiroControlResponseSummary(record(ELICIT, { action: 'accept', content: { size: 'Large' } }))).toBeNull()
  })

  it('shows the reason a rejected permission carried', () => {
    expect(kiroControlResponseSummary(record(PERMISSION, { outcome: { outcome: 'selected', optionId: 'reject' }, _meta: { kiro: { rejectionReason: ' Use rg ' } } })))
      .toEqual({ kind: 'feedback', message: 'Use rg' })
  })

  it('shows the scope of an always-allow in both forms a saved reply can hold', () => {
    expect(kiroControlResponseSummary(record(PERMISSION, { outcome: { outcome: 'selected', optionId: 'always-accept' }, _meta: { kiro: { consent: { scope: 'workspace' } } } })))
      .toEqual({ kind: 'label', text: 'Always allow in this workspace' })
    expect(kiroControlResponseSummary(record(PERMISSION, { outcome: { outcome: 'selected', optionId: 'always-accept-user' } })))
      .toEqual({ kind: 'label', text: 'Always allow everywhere' })
    expect(kiroControlResponseSummary(record(PERMISSION, { outcome: { outcome: 'selected', optionId: 'always-accept' } })))
      .toEqual({ kind: 'label', text: 'Always allow for this session' })
    expect(kiroControlResponseSummary(record(PERMISSION, { outcome: { outcome: 'selected', optionId: 'always-accept' }, _meta: { kiro: { consent: { scope: 'toString' } } } })))
      .toEqual({ kind: 'label', text: 'Always allow for this session' })
  })

  it('shows the scope of an always-deny in both forms a saved reply can hold', () => {
    expect(kiroControlResponseSummary(record(PERMISSION, { outcome: { outcome: 'selected', optionId: 'always-reject' }, _meta: { kiro: { consent: { scope: 'workspace' } } } })))
      .toEqual({ kind: 'label', text: 'Always deny in this workspace' })
    expect(kiroControlResponseSummary(record(PERMISSION, { outcome: { outcome: 'selected', optionId: 'always-reject-user' } })))
      .toEqual({ kind: 'label', text: 'Always deny everywhere' })
    expect(kiroControlResponseSummary(record(PERMISSION, { outcome: { outcome: 'selected', optionId: 'always-reject' } })))
      .toEqual({ kind: 'label', text: 'Always deny for this session' })
  })

  it('shows the chosen option of a permission', () => {
    expect(kiroControlResponseSummary(record(PERMISSION, { outcome: { outcome: 'selected', optionId: 'accept' } }))).toEqual({ kind: 'label', text: 'Allow' })
  })

  // A reason of only whitespace is no reason, so the row states the button instead.
  it('shows the rule of an always option when the rejection reason is only whitespace', () => {
    expect(kiroControlResponseSummary(record(PERMISSION, { outcome: { outcome: 'selected', optionId: 'always-reject' }, _meta: { kiro: { rejectionReason: '  ', consent: { scope: 'user' } } } })))
      .toEqual({ kind: 'label', text: 'Always deny everywhere' })
  })

  it('shows the user scope of Kiro\'s own always-allow', () => {
    expect(kiroControlResponseSummary(record(PERMISSION, { outcome: { outcome: 'selected', optionId: 'always-accept' }, _meta: { kiro: { consent: { scope: 'user' } } } })))
      .toEqual({ kind: 'label', text: 'Always allow everywhere' })
  })

  it('shows no answer of a question that saved no answer', () => {
    expect(kiroControlResponseSummary(record(QUESTION, { action: 'answered', answer: '  ' }))).toBeNull()
    expect(kiroControlResponseSummary(record(QUESTION, { action: 'answered' }))).toBeNull()
  })

  it('falls back to the shared display when the reply is no result', () => {
    expect(kiroControlResponseSummary(record(QUESTION, undefined))).toBeNull()
  })
})
