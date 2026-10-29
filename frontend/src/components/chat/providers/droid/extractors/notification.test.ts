import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it } from 'vitest'
import { droidCompactionBoundary, droidNotificationEntry } from './notification'

function parsed(message: Record<string, unknown>): ParsedMessageContent {
  return {
    rawText: JSON.stringify(message),
    topLevel: message,
    parentObject: message,
    wrapper: null,
  }
}

describe('droidCompactionBoundary', () => {
  it('reads a standalone native boundary', () => {
    expect(droidCompactionBoundary(parsed({ type: 'session_compacted', removedCount: 3 }))).toEqual({})
  })

  it('finds a boundary after another notice in a stored thread', () => {
    const wrapper = {
      type: 'notification_thread',
      old_seqs: [],
      messages: [
        { type: 'settings_updated' },
        { type: 'session_compacted', removedCount: 3 },
      ],
    }
    expect(droidCompactionBoundary({
      rawText: JSON.stringify(wrapper),
      topLevel: wrapper,
      parentObject: wrapper,
      wrapper,
    })).toEqual({})
  })

  it('ignores a row with no compaction event', () => {
    expect(droidCompactionBoundary(parsed({ type: 'settings_updated' }))).toBeNull()
  })
})

describe('droidNotificationEntry', () => {
  it('leaves unknown native frames for the no-display card', () => {
    expect(droidNotificationEntry({
      type: 'session_token_usage_changed',
      tokenUsage: { used: 12 },
    })).toEqual([])
    expect(droidNotificationEntry({ type: 'unknown_native_event', value: 1 })).toEqual([])
  })

  it('shows native errors without losing a nested or unknown diagnostic', () => {
    expect(droidNotificationEntry({ type: 'error', message: 'The tool failed.' })).toEqual([
      { kind: 'text', text: 'The tool failed.' },
    ])
    expect(droidNotificationEntry({
      type: 'error',
      error: { message: 'Nested failure.' },
    })).toEqual([
      { kind: 'text', text: 'Nested failure.' },
    ])
    expect(droidNotificationEntry({ type: 'error' })).toEqual([])
  })

  it('reads a retry with a reason label and validates its count', () => {
    expect(droidNotificationEntry({ type: 'llm_retry', attempt: 2, reason: 'overloaded' })).toEqual([
      {
        kind: 'retry',
        scope: 'api',
        attempt: 2,
        error: 'server overloaded',
      },
    ])
    expect(droidNotificationEntry({ type: 'llm_retry', attempt: -1 })).toEqual([
      { kind: 'retry', scope: 'api' },
    ])
  })

  it('reads a failed hook and a blocked hook', () => {
    expect(droidNotificationEntry({
      type: 'hook_execution_completed',
      hookEventName: 'Stop',
      hookStatus: 'error',
      hookResults: [
        { command: 'check', exitCode: 2, stderr: 'Check failed' },
      ],
    })).toEqual([
      {
        kind: 'hook',
        outcome: 'failed',
        event: 'Stop',
        name: 'check',
        exitCode: 2,
        detail: 'Check failed',
      },
    ])
    expect(droidNotificationEntry({ type: 'hook_execution_completed', hookPreventedAction: true }))
      .toEqual([
        { kind: 'hook', outcome: 'blocked' },
      ])
  })

  it('keeps incomplete hook diagnostics and does not turn a success into a failure', () => {
    expect(droidNotificationEntry({
      type: 'hook_execution_completed',
      hookStatus: 'error',
      hookResults: [
        { command: 'check', stderr: 'The hook timed out' },
      ],
    })).toEqual([
      {
        kind: 'hook',
        outcome: 'failed',
        name: 'check',
        detail: 'The hook timed out',
      },
    ])
    expect(droidNotificationEntry({ type: 'hook_execution_completed', hookStatus: 'completed' })).toEqual([])
    expect(droidNotificationEntry({ type: 'hook_execution_completed' })).toEqual([])
  })

  it.each([
    -1,
    0.5,
    Number.NaN,
    Number.POSITIVE_INFINITY,
  ])('omits an invalid retry count %s', (attempt) => {
    expect(droidNotificationEntry({ type: 'llm_retry', attempt, reason: '  ' })).toEqual([
      { kind: 'retry', scope: 'api' },
    ])
  })

  it('reads zero and an unknown retry reason without an inherited property', () => {
    expect(droidNotificationEntry({ type: 'llm_retry', attempt: 0, reason: 'constructor' }))
      .toEqual([
        {
          kind: 'retry',
          scope: 'api',
          attempt: 0,
          error: 'constructor',
        },
      ])
  })

  it('reads only failed mcp servers', () => {
    expect(droidNotificationEntry({
      type: 'mcp_status_changed',
      servers: [
        { name: 'ready', status: 'connected' },
        { name: 'auth', status: 'failed', requiresAuth: true },
        { name: 'broken', status: 'failed', error: 'Connection closed' },
      ],
    })).toEqual([
      { kind: 'text', text: 'MCP server auth needs authentication' },
      { kind: 'text', text: 'MCP server broken failed: Connection closed' },
    ])
    expect(droidNotificationEntry({ type: 'mcp_status_changed', servers: [] })).toEqual([])
  })

  it('reads an untracked discard and a user-only system notice', () => {
    expect(droidNotificationEntry({ type: 'queued_messages_discarded', text: 'guide' }))
      .toEqual([
        { kind: 'text', text: 'Droid discarded a queued message: guide' },
      ])
    expect(droidNotificationEntry({
      type: 'create_message',
      message: {
        role: 'system',
        visibility: 'user_only',
        content: [
          { type: 'text', text: 'Native notice' },
        ],
      },
    })).toEqual([
      { kind: 'text', text: 'Native notice' },
    ])
  })

  it('marks a native compaction as completed', () => {
    expect(droidNotificationEntry({ type: 'session_compacted' }))
      .toEqual([
        { kind: 'compaction', phase: 'end', outcome: 'completed' },
      ])
  })
})
