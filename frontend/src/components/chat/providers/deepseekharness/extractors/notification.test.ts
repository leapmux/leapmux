import { describe, expect, it } from 'vitest'
import { deepseekHarnessNotificationEntry } from './notification'

const event = (type: string, data: Record<string, unknown>) => ({ type, data })

describe('deepseekHarnessNotificationEntry', () => {
  it('reads a failed compaction as the failed outcome', () => {
    expect(deepseekHarnessNotificationEntry(event('compaction/end', { error: 'native failure' }))).toEqual([{ kind: 'compaction', phase: 'end', outcome: 'failed', error: 'native failure' }])
    expect(deepseekHarnessNotificationEntry(event('compaction/end', {}))).toEqual([{ kind: 'compaction', phase: 'end', outcome: 'completed', detail: {} }])
  })

  it('builds llm/retry through retryEntry', () => {
    expect(deepseekHarnessNotificationEntry(event('llm/retry', { retry: 2, maxRetries: 5, delayMs: 50, failure: { message: 'overloaded' } }))).toEqual([{ kind: 'retry', scope: 'api', attempt: 2, maxAttempts: 5, delayMs: 50, error: 'overloaded' }])
    expect(deepseekHarnessNotificationEntry(event('llm/retry', { retry: -1, maxRetries: NaN, delayMs: Infinity, failure: { message: '  ' } }))).toEqual([{ kind: 'retry', scope: 'api' }])
    expect(deepseekHarnessNotificationEntry(event('llm/retry', { retry: 0, maxRetries: 0, delayMs: 0 }))).toEqual([{ kind: 'retry', scope: 'api', attempt: 0, maxAttempts: 0, delayMs: 0 }])
  })

  it.each([
    [{ decision: 'block', point: 'PreToolUse', handlerId: 'check', exitCode: 2, stderrSummary: 'refused' }, { kind: 'hook', outcome: 'blocked', event: 'PreToolUse', name: 'check', exitCode: 2, detail: 'refused' }],
    [{ decision: 'deny' }, { kind: 'hook', outcome: 'blocked' }],
    [{ decision: 'stop' }, { kind: 'hook', outcome: 'blocked' }],
    [{ decision: 'pass', exitCode: 1 }, { kind: 'hook', outcome: 'failed', exitCode: 1 }],
    [{ decision: 'pass', stderrSummary: 'cannot start the hook' }, { kind: 'hook', outcome: 'failed', detail: 'cannot start the hook' }],
  ])('reads a failed hook result', (data, entry) => {
    expect(deepseekHarnessNotificationEntry(event('hook/result', data))).toEqual([entry])
  })

  it('does not invent a failure for a successful hook', () => {
    expect(deepseekHarnessNotificationEntry(event('hook/result', { decision: 'pass', exitCode: 0, stderrSummary: 'informational output' }))).toEqual([])
    expect(deepseekHarnessNotificationEntry(event('hook/result', { decision: 'pass' }))).toEqual([])
  })

  it('reads the native deliverables without serializing their objects', () => {
    expect(deepseekHarnessNotificationEntry(event('deliverables/presented', { files: [{ path: 'report.md', description: 'Report' }, { path: 'data.csv' }, null, { description: 'No path' }] }))).toEqual([{ kind: 'text', text: 'Presented files:\nreport.md (Report)\ndata.csv' }])
    expect(deepseekHarnessNotificationEntry(event('deliverables/presented', {}))).toEqual([{ kind: 'text', text: 'Deliverables presented' }])
  })

  it('leaves unknown or malformed events unclaimed', () => {
    expect(deepseekHarnessNotificationEntry(event('future/event', {}))).toEqual([])
    expect(deepseekHarnessNotificationEntry({ type: 'compaction/end', data: 'invalid' })).toEqual([])
  })
})
