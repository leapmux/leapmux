import { describe, expect, it } from 'vitest'
import { piSubagentNotificationSources, piVisibleCustomMessage } from './customMessage'

function notification(content: string, details: Record<string, unknown> = {}) {
  return { type: 'message_end', message: { role: 'custom', customType: 'subagent-notification', display: true, content, details: { id: 'child', status: 'completed', resultPreview: 'Preview', ...details } } }
}

describe('pi custom messages', () => {
  it.each([
    '<task-notification><task-id>other</task-id><result>Wrong child</result></task-notification>',
    '<task-notification><task-id>child</task-id><result>Unescaped & text</result></task-notification>',
    '<task-notification><task-id>child</task-id><result><span>Unexpected markup</span></result></task-notification>',
    '<task-notification><task-id>child</task-id><result>First</result><result>Second</result></task-notification>',
    '<task-notification><task-id>child</task-id><result>First</result></task-notification><task-notification><task-id>child</task-id><result>Second</result></task-notification>',
  ])('keeps the preview when the full report is invalid or ambiguous: %s', (content) => {
    expect(piSubagentNotificationSources(notification(content))?.[0].body).toBe('Preview')
  })

  it('keeps an explicitly empty full report', () => {
    expect(piSubagentNotificationSources(notification('<task-notification><task-id>child</task-id><result></result></task-notification>'))?.[0].body).toBe('')
  })

  it('renders every valid group entry with its own outcome', () => {
    const sources = piSubagentNotificationSources(notification('', { others: [null, { id: 'failed-child', status: 'error', error: 'Missing source', resultPreview: 'Partial report' }, { id: 'stopped-child', status: 'stopped' }] }))
    expect(sources?.map(source => [source.agentId, source.outcome])).toEqual([['child', 'completed'], ['failed-child', 'failed'], ['stopped-child', 'stopped']])
    expect(sources?.[1].metadata).toContainEqual({ label: 'Error', value: 'Missing source' })
  })

  it('preserves zero counters and rejects invalid counters', () => {
    const sources = piSubagentNotificationSources(notification('', { toolUses: 0, totalTokens: -1, turnCount: 1.5, durationMs: 0 }))
    expect(sources?.[0].metadata).toEqual([{ label: 'Agent ID', value: 'child' }, { label: 'Tool uses', value: '0' }, { label: 'Duration', value: '0ms' }])
  })

  it('does not render hidden or non-custom messages', () => {
    const payload = notification('Hidden')
    expect(piVisibleCustomMessage({ ...payload, message: { ...payload.message, display: false } })).toBeNull()
    expect(piSubagentNotificationSources({ ...payload, message: { ...payload.message, role: 'assistant' } })).toBeNull()
    expect(piSubagentNotificationSources({ ...payload, type: 'message_start' })).toBeNull()
  })

  it('leaves malformed notification details to the custom text renderer', () => {
    expect(piSubagentNotificationSources(notification('Preserve this text', { id: null }))).toBeNull()
    expect(piSubagentNotificationSources(notification('Preserve this text', { status: '' }))).toBeNull()
  })
})
