import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { renderNotificationThread } from '../../../notificationRenderers'
import { renderThreadHasIcon, renderThreadText } from '../../../notificationTestUtils'
import { describePiNotification, piNotificationThreadEntry } from './notification'

// Side-effect import to register the Pi plugin so renderNotificationThread can
// consult its notificationThreadEntry -- the sole Pi notification render path.
await import('../plugin')

// A single Pi notification renders as a one-element thread under the Pi provider,
// the path MessageBubble uses for a standalone notification.
const renderPiText = (parsed: unknown): string => renderThreadText([parsed], AgentProvider.PI)
const rendersWithIcon = (parsed: unknown): boolean => renderThreadHasIcon([parsed], AgentProvider.PI)

describe('describePiNotification', () => {
  describe('compaction events', () => {
    // Pi compaction shares the provider-neutral "Context compacted (reason, pre)"
    // format with Claude/Codex. compaction_start is the in-progress spinner label;
    // compaction_end carries a reason (the trigger) and `result.tokensBefore` (the
    // pre-compaction size). Pi has no post count, so the transition is pre-only.

    it('renders compaction_start as the shared in-progress label, regardless of reason', () => {
      expect(describePiNotification({ type: 'compaction_start', reason: 'manual' }))
        .toBe('Compacting context...')
      expect(describePiNotification({ type: 'compaction_start', reason: 'threshold' }))
        .toBe('Compacting context...')
      expect(describePiNotification({ type: 'compaction_start', reason: 'mystery' }))
        .toBe('Compacting context...')
    })

    it('renders compaction_end as "Context compacted (reason, pre)" with the pre size', () => {
      expect(describePiNotification({
        type: 'compaction_end',
        reason: 'threshold',
        result: { tokensBefore: 12345 },
      })).toBe('Context compacted (threshold, 12.3k)')
    })

    it('carries the reason through as the trigger for every reason value', () => {
      expect(describePiNotification({ type: 'compaction_end', reason: 'manual', result: { tokensBefore: 200000 } }))
        .toBe('Context compacted (manual, 200.0k)')
      expect(describePiNotification({ type: 'compaction_end', reason: 'overflow', result: { tokensBefore: 200000 } }))
        .toBe('Context compacted (overflow, 200.0k)')
    })

    it('renders the reason alone when tokensBefore is absent', () => {
      expect(describePiNotification({ type: 'compaction_end', reason: 'manual' }))
        .toBe('Context compacted (manual)')
    })

    it('rounds a fractional pre size from result.tokensBefore', () => {
      // result.tokensBefore is a server-side estimate that could be non-integer;
      // the shared formatter must round it rather than leak decimals.
      expect(describePiNotification({ type: 'compaction_end', reason: 'manual', result: { tokensBefore: 512.7 } }))
        .toBe('Context compacted (manual, 513)')
    })

    it('shows the pre size alone when the reason is absent', () => {
      expect(describePiNotification({ type: 'compaction_end', result: { tokensBefore: 12345 } }))
        .toBe('Context compacted (12.3k)')
    })

    it('renders a bare "Context compacted" when neither reason nor tokensBefore is present', () => {
      expect(describePiNotification({ type: 'compaction_end' }))
        .toBe('Context compacted')
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

describe('pi single-notification rendering (markup)', () => {
  // A standalone Pi notification renders through the same renderNotificationThread
  // path as a consolidated one. Pi emits compaction boundaries as `divider` thread
  // entries, which the shared renderer draws with its compaction-divider row, so Pi
  // matches Claude/Codex visually (icon + label); every other Pi notification is a
  // plain `text` entry rendered as a line.
  it('renders compaction_start as a divider with the spinner icon', () => {
    const msg = { type: 'compaction_start', reason: 'manual' }
    expect(rendersWithIcon(msg)).toBe(true)
    expect(renderPiText(msg)).toBe('Compacting context...')
  })

  it('renders a completed compaction_end as a divider with the icon', () => {
    const msg = { type: 'compaction_end', reason: 'threshold', result: { tokensBefore: 12345 } }
    expect(rendersWithIcon(msg)).toBe(true)
    expect(renderPiText(msg)).toBe('Context compacted (threshold, 12.3k)')
  })

  it('renders an aborted compaction as a plain line, not a divider', () => {
    // An aborted compaction produced no boundary, so it has no divider icon.
    const msg = { type: 'compaction_end', aborted: true, reason: 'threshold' }
    expect(rendersWithIcon(msg)).toBe(false)
    expect(renderPiText(msg)).toBe('Context compaction aborted')
  })

  it('renders non-compaction notifications as plain text without an icon', () => {
    const msg = { type: 'auto_retry_end', success: true, attempt: 2 }
    expect(rendersWithIcon(msg)).toBe(false)
    expect(renderPiText(msg)).toBe('Auto-retry succeeded (attempt 2)')
  })

  it('renders nothing for a shape neither Pi nor the shared switch owns', () => {
    expect(renderPiText({ type: 'totally_unknown_pi_event' })).toBe('')
  })
})

describe('piNotificationThreadEntry', () => {
  // The sole Pi notification render seam: compaction boundaries become divider
  // entries, everything else a text entry, and unowned shapes return null so the
  // shared switch can try them.
  it('maps compaction_start to a loading divider entry', () => {
    expect(piNotificationThreadEntry({ type: 'compaction_start', reason: 'manual' }))
      .toEqual([{ kind: 'divider', text: 'Compacting context...', loading: true }])
  })

  it('maps a completed compaction_end to a non-loading divider entry', () => {
    expect(piNotificationThreadEntry({ type: 'compaction_end', reason: 'threshold', result: { tokensBefore: 12345 } }))
      .toEqual([{ kind: 'divider', text: 'Context compacted (threshold, 12.3k)', loading: false }])
  })

  it('maps an aborted compaction_end to a plain text entry (no boundary)', () => {
    expect(piNotificationThreadEntry({ type: 'compaction_end', aborted: true, reason: 'threshold' }))
      .toEqual([{ kind: 'text', text: 'Context compaction aborted' }])
  })

  it('maps auto_retry to a text entry', () => {
    expect(piNotificationThreadEntry({ type: 'auto_retry_end', success: true, attempt: 2 }))
      .toEqual([{ kind: 'text', text: 'Auto-retry succeeded (attempt 2)' }])
  })

  it('returns null for shapes Pi does not own, so the shared switch can try them', () => {
    expect(piNotificationThreadEntry({ type: 'settings_changed' })).toBeNull()
  })
})

describe('pi notification thread: no multi-event truncation', () => {
  // Regression guard: a consolidated wrapper of multiple Pi notifications must
  // render EVERY entry, not just the first. Before the Pi notificationThreadEntry
  // wiring, renderNotificationThread had no Pi branch and MessageBubble showed
  // only messages[0], silently dropping the rest.
  it('renders both an auto_retry and a following compaction boundary', () => {
    const text = renderThreadText([
      { type: 'auto_retry_start', attempt: 1, maxAttempts: 3, delayMs: 2000 },
      { type: 'compaction_end', reason: 'threshold', result: { tokensBefore: 12345 } },
    ], AgentProvider.PI)
    expect(text).toContain('Auto-retry 1/3')
    expect(text).toContain('Context compacted (threshold, 12.3k)')
  })

  it('renders every boundary in a wrapper of two compaction_end events', () => {
    const el = renderNotificationThread([
      { type: 'compaction_end', reason: 'manual', result: { tokensBefore: 100000 } },
      { type: 'compaction_end', reason: 'manual', result: { tokensBefore: 50000 } },
    ], AgentProvider.PI)
    const { container } = render(() => el)
    // Two boundary dividers, each with its own icon -- not collapsed to one.
    expect(container.querySelectorAll('svg').length).toBe(2)
    expect(container.textContent).toContain('100.0k')
    expect(container.textContent).toContain('50.0k')
  })
})
