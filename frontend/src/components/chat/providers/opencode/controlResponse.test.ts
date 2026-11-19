import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { openCodeControlResponseSummary } from './controlResponse'

function response(native: Record<string, unknown>): PersistedControlResponse {
  return {
    claimToken: 'claim',
    requestId: 'form_1',
    request: {
      type: 'form.created',
      data: {
        form: {
          fields: [
            { key: 'choice', type: 'string', title: 'Choice' },
            { key: 'count', type: 'integer', title: 'Count' },
            { key: 'enabled', type: 'boolean', title: 'Enabled' },
          ],
        },
      },
    },
    response: { request_id: 'form_1', ...native },
  }
}

describe('openCodeControlResponseSummary', () => {
  it('renders scalar native question answers with their field titles', () => {
    expect(openCodeControlResponseSummary(response({
      answer: { choice: 'Green' },
    })))
      .toEqual({ kind: 'label', text: 'Choice: Green' })
  })

  it('retains zero, false, empty strings, and empty selection lists', () => {
    expect(openCodeControlResponseSummary(response({
      answer: {
        count: 0,
        enabled: false,
        choice: '',
        extras: [],
      },
    })))
      .toEqual({ kind: 'label', text: 'Count: 0\nEnabled: false\nChoice: ""\nextras: []' })
  })

  it.each([
    ['once', 'Allowed'],
    ['always', 'Always allowed'],
    ['reject', 'Denied'],
  ])('describes native permission decision %s', (optionId, text) => {
    expect(openCodeControlResponseSummary(response({
      decision: optionId,
    }))).toEqual({ kind: 'label', text })
  })

  it('describes cancellation and unanswered forms', () => {
    expect(openCodeControlResponseSummary(response({ cancel: true }))).toEqual({ kind: 'label', text: 'Cancelled' })
    expect(openCodeControlResponseSummary(response({ answer: {} }))).toEqual({ kind: 'label', text: 'Answered' })
    expect(openCodeControlResponseSummary(response({ unexpected: true }))).toBeNull()
  })
})
