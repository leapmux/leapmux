import { describe, expect, it } from 'vitest'
import { isTerminalCompactingStatus } from './messageUtils'

// isTerminalCompactingStatus is the single source of truth shared by the Claude,
// Codex, and ACP hidden-notification predicates (standalone + consolidated-thread
// paths). These cases pin the exact shape each provider relies on so the rule
// can't drift out from under any of them.
describe('isTerminalCompactingStatus', () => {
  it('is true for a terminal compaction status with status:null', () => {
    expect(isTerminalCompactingStatus({ type: 'system', subtype: 'status', status: null })).toBe(true)
  })

  it('is true for a terminal status carrying compact_result', () => {
    expect(isTerminalCompactingStatus({ type: 'system', subtype: 'status', status: null, compact_result: 'success' })).toBe(true)
  })

  it('is true for any non-compacting status string', () => {
    expect(isTerminalCompactingStatus({ type: 'system', subtype: 'status', status: 'done' })).toBe(true)
  })

  it('is true when the status field is absent (undefined !== "compacting")', () => {
    expect(isTerminalCompactingStatus({ type: 'system', subtype: 'status' })).toBe(true)
  })

  it('is false for the live "compacting" status (the one visible row)', () => {
    expect(isTerminalCompactingStatus({ type: 'system', subtype: 'status', status: 'compacting' })).toBe(false)
  })

  it('is false for a non-status system subtype', () => {
    expect(isTerminalCompactingStatus({ type: 'system', subtype: 'compact_boundary' })).toBe(false)
  })

  it('is false for a non-system message type', () => {
    expect(isTerminalCompactingStatus({ type: 'settings_changed', subtype: 'status', status: null })).toBe(false)
  })
})
