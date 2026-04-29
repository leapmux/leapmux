import { describe, expect, it } from 'vitest'
import { describePiNotification } from './notification'

describe('describePiNotification', () => {
  describe('compaction events', () => {
    it('renders manual compaction_start as "Manually compacted context…"', () => {
      expect(describePiNotification({ type: 'compaction_start', reason: 'manual' }))
        .toBe('Manually compacted context…')
    })

    it('renders threshold compaction_start with default suffix', () => {
      expect(describePiNotification({ type: 'compaction_start', reason: 'threshold' }))
        .toBe('Compacted context after threshold…')
    })

    it('falls back to a generic message when reason is unknown', () => {
      expect(describePiNotification({ type: 'compaction_start', reason: 'mystery' }))
        .toBe('Compacting context…')
    })

    it('renders compaction_end with token count', () => {
      expect(describePiNotification({
        type: 'compaction_end',
        reason: 'threshold',
        result: { tokensBefore: 12345 },
      })).toBe('Compacted context after threshold (was 12,345 tokens)')
    })

    it('renders compaction_end without tokensBefore', () => {
      expect(describePiNotification({ type: 'compaction_end', reason: 'manual' }))
        .toBe('Manually compacted context')
    })

    it('flags aborted compaction explicitly', () => {
      expect(describePiNotification({ type: 'compaction_end', aborted: true, reason: 'threshold' }))
        .toBe('Context compaction aborted')
    })
  })

  describe('auto_retry events', () => {
    it('renders auto_retry_start with attempt + delay', () => {
      expect(describePiNotification({
        type: 'auto_retry_start',
        attempt: 1,
        maxAttempts: 3,
        delayMs: 2000,
      })).toBe('Auto-retry 1/3 in 2s…')
    })

    it('renders auto_retry_start with errorMessage in place of "…"', () => {
      expect(describePiNotification({
        type: 'auto_retry_start',
        attempt: 1,
        maxAttempts: 3,
        delayMs: 2000,
        errorMessage: 'overloaded',
      })).toBe('Auto-retry 1/3 in 2s — overloaded')
    })

    it('renders auto_retry_end success with attempt number', () => {
      expect(describePiNotification({ type: 'auto_retry_end', success: true, attempt: 2 }))
        .toBe('Auto-retry succeeded (attempt 2)')
    })

    it('renders auto_retry_end failure with finalError', () => {
      expect(describePiNotification({ type: 'auto_retry_end', success: false, finalError: 'gave up' }))
        .toBe('Auto-retry failed: gave up')
    })

    it('renders auto_retry_end failure without finalError', () => {
      expect(describePiNotification({ type: 'auto_retry_end', success: false }))
        .toBe('Auto-retry failed')
    })
  })

  describe('extension_error', () => {
    it('renders all fields when present', () => {
      expect(describePiNotification({
        type: 'extension_error',
        extensionPath: '/path/ext.ts',
        event: 'tool_call',
        error: 'boom',
      })).toBe('Extension error in /path/ext.ts (tool_call): boom')
    })

    it('drops missing fields gracefully', () => {
      expect(describePiNotification({ type: 'extension_error' }))
        .toBe('Extension error')
    })
  })

  describe('extension_ui_request notify (Phase 4.5 raw-passthrough form)', () => {
    it('renders the message field from the raw envelope', () => {
      expect(describePiNotification({
        type: 'extension_ui_request',
        method: 'notify',
        notifyType: 'warning',
        message: 'Hello world',
      })).toBe('Hello world')
    })

    it('renders a method-name label for unknown methods so the raw passthrough stays visible', () => {
      // The worker persists `extension_ui_request` for any method whose
      // routing isn't dialog/setStatus/setWidget/setTitle/set_editor_text
      // (default case, "so the user can see it"). The renderer must not
      // drop these to invisible — fall back to a generic label.
      expect(describePiNotification({
        type: 'extension_ui_request',
        method: 'someUnknownMethod',
      })).toBe('Extension UI: someUnknownMethod')
    })

    it('renders a generic label when the extension_ui_request lacks any method', () => {
      expect(describePiNotification({ type: 'extension_ui_request' }))
        .toBe('Extension UI request')
    })

    it('returns null when message is empty', () => {
      expect(describePiNotification({
        type: 'extension_ui_request',
        method: 'notify',
        message: '',
      })).toBeNull()
    })

    it('returns null for the legacy synthesized {type:"agent_notify"} envelope (deprecated)', () => {
      // Phase 4.5 stops emitting this shape; legacy DB rows fall back to
      // the shared notification-thread describer or the raw-JSON bubble.
      expect(describePiNotification({
        type: 'agent_notify',
        level: 'warning',
        message: 'Hello',
      })).toBeNull()
    })
  })

  it('returns null for shapes the describer does not own', () => {
    expect(describePiNotification({ type: 'settings_changed' })).toBeNull()
    expect(describePiNotification({ type: 'context_cleared' })).toBeNull()
    expect(describePiNotification(null)).toBeNull()
    expect(describePiNotification('not an object')).toBeNull()
    expect(describePiNotification(undefined)).toBeNull()
  })
})
