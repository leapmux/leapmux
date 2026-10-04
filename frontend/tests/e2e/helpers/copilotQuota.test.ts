import { describe, expect, it } from 'vitest'
import { copilotQuotaHeaders } from './copilotQuota'

describe('copilotQuotaHeaders', () => {
  it.each(['premium_interactions', 'chat', 'completions'])('encodes the native query fields for %s', (type) => {
    const headers = copilotQuotaHeaders({ type, status: 'allowed_warning', utilization: 0.73, resetsAt: 0 })
    expect(headers[`x-quota-snapshot-${type}`]).toBe('ent=100&rem=27&ov=0&ovPerm=false&rst=1970-01-01T00:00:00.000Z')
  })

  it('keeps unused and exhausted quotas distinct and leaves an absent reset absent', () => {
    const unused = new URLSearchParams(copilotQuotaHeaders({ type: 'chat', status: 'allowed', utilization: 0 })['x-quota-snapshot-chat'])
    const exhausted = new URLSearchParams(copilotQuotaHeaders({ type: 'chat', status: 'exceeded', utilization: 1 })['x-quota-snapshot-chat'])
    expect(unused.get('rem')).toBe('100')
    expect(exhausted.get('rem')).toBe('0')
    expect(unused.has('rst')).toBe(false)
    expect(exhausted.has('rst')).toBe(false)
  })

  it('uses unused quota when the neutral utilization field is absent', () => {
    expect(copilotQuotaHeaders({ type: 'chat', status: 'allowed' })).toEqual({ 'x-quota-snapshot-chat': 'ent=100&rem=100&ov=0&ovPerm=false' })
  })

  it.each(['five_hour', '', 'chat\nprivate', 'chat:private', 'chat/other'])('ignores the unsupported native resource %s', (type) => {
    expect(copilotQuotaHeaders({ type, status: 'allowed' })).toEqual({})
  })

  it.each([Number.NaN, Number.POSITIVE_INFINITY, -0.01, 1.01])('refuses the invalid utilization %s', (utilization) => {
    expect(() => copilotQuotaHeaders({ type: 'chat', status: 'allowed', utilization })).toThrow('utilization')
  })

  it('refuses a reset date that the native protocol cannot encode', () => {
    expect(() => copilotQuotaHeaders({ type: 'chat', status: 'allowed', resetsAt: Number.MAX_SAFE_INTEGER })).toThrow()
  })
})
