import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { reasonixControlResponseSummary } from './controlResponse'

function record(request: Record<string, unknown>, optionId: string): PersistedControlResponse {
  return { requestId: 'jsonrpc:3', claimToken: 'claim', request, response: { result: { outcome: { outcome: 'selected', optionId } } } }
}

const REQUEST = {
  method: 'session/request_permission',
  params: {
    toolCall: { title: 'exit_plan_mode', _meta: { 'reasonix.io': { tool: 'exit_plan_mode' } } },
    options: [
      { optionId: 'allow_once', name: 'Allow', kind: 'allow_once' },
      { optionId: 'reject_once', name: 'Reject', kind: 'reject_once' },
    ],
  },
}

describe('reasonixControlResponseSummary', () => {
  it.each([
    ['allow_once', 'Approve'],
    ['reject_once', 'Reject'],
  ])('shows the plan decision for %s', (optionId, text) => {
    expect(reasonixControlResponseSummary(record(REQUEST, optionId))).toEqual({ kind: 'label', text })
  })

  it('keeps an unknown native option readable', () => {
    expect(reasonixControlResponseSummary(record(REQUEST, 'future_choice'))).toEqual({ kind: 'label', text: 'future_choice' })
  })

  it('delegates ordinary permissions', () => {
    const request = { ...REQUEST, params: { ...REQUEST.params, toolCall: { title: 'bash', _meta: { 'reasonix.io': { tool: 'bash' } } } } }
    expect(reasonixControlResponseSummary(record(request, 'allow_once'))).toEqual({ kind: 'label', text: 'Allow' })
  })
})
