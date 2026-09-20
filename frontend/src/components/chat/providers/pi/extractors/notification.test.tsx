import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { renderThreadElement, renderThreadHasIcon, renderThreadText } from '~/test-support/messageRenderProbes'
import { describePiNotification, piNotificationEntry } from './notification'

// Side-effect import to register the Pi plugin so renderNotificationThread can
// consult its notificationThreadEntry -- the sole Pi notification render path.
await import('../plugin')

// A single Pi notification renders as a one-element thread under the Pi provider,
// the path MessageBubble uses for a standalone notification.
const renderPiText = (parsed: unknown): string => renderThreadText([parsed], AgentProvider.PI)
const rendersWithIcon = (parsed: unknown): boolean => renderThreadHasIcon([parsed], AgentProvider.PI)

describe('piNotificationEntry compaction and retries', () => {
  // Pi's compaction pair and its two retry families are STRUCTURED entries, so the
  // shared formatter writes their sentences. That is what makes a stall on Pi read the
  // same as a stall on Claude, where the two wordings used to differ.
  const line = (event: Record<string, unknown>): string => renderPiText(event)

  it('draws compaction_start as the shared in-progress label, whatever the reason', () => {
    for (const reason of ['manual', 'threshold', 'mystery'])
      expect(line({ type: 'compaction_start', reason })).toBe('Compacting context...')
  })

  it('draws compaction_end as "Context compacted (reason, pre)" with the pre size', () => {
    expect(line({ type: 'compaction_end', reason: 'threshold', result: { tokensBefore: 12345 } }))
      .toBe('Context compacted (threshold, 12.3k)')
  })

  it('carries the reason through as the trigger for every reason value', () => {
    expect(line({ type: 'compaction_end', reason: 'manual', result: { tokensBefore: 200000 } }))
      .toBe('Context compacted (manual, 200.0k)')
    expect(line({ type: 'compaction_end', reason: 'overflow', result: { tokensBefore: 200000 } }))
      .toBe('Context compacted (overflow, 200.0k)')
  })

  it('draws the reason alone when tokensBefore is absent', () => {
    expect(line({ type: 'compaction_end', reason: 'manual' })).toBe('Context compacted (manual)')
  })

  // `result.tokensBefore` is an estimate that can be non-integer; the shared
  // formatter rounds it rather than leaking decimals.
  it('rounds a fractional pre size from result.tokensBefore', () => {
    expect(line({ type: 'compaction_end', reason: 'manual', result: { tokensBefore: 512.7 } }))
      .toBe('Context compacted (manual, 513)')
  })

  it('draws the pre size alone when the reason is absent', () => {
    expect(line({ type: 'compaction_end', result: { tokensBefore: 12345 } })).toBe('Context compacted (12.3k)')
  })

  it('draws a bare "Context compacted" when neither reason nor tokensBefore is present', () => {
    expect(line({ type: 'compaction_end' })).toBe('Context compacted')
  })

  it('flags an aborted compaction explicitly', () => {
    expect(line({ type: 'compaction_end', aborted: true, reason: 'threshold' })).toBe('Context compaction aborted')
  })

  it('draws auto_retry_start with the attempt and the wait', () => {
    expect(line({ type: 'auto_retry_start', attempt: 1, maxAttempts: 3, delayMs: 2000 })).toBe('API retry 1/3 in 2s')
  })

  it('draws auto_retry_start with the error beside the wait', () => {
    expect(line({ type: 'auto_retry_start', attempt: 1, maxAttempts: 3, delayMs: 2000, errorMessage: 'overloaded' }))
      .toBe('API retry 1/3 in 2s (overloaded)')
  })

  it('draws a retry that worked as an end to the stall', () => {
    expect(line({ type: 'auto_retry_end', success: true, attempt: 2 })).toBe('API retry 2 succeeded')
  })

  it('draws a retry that gave up, with its final error', () => {
    expect(line({ type: 'auto_retry_end', success: false, finalError: 'gave up' })).toBe('API retry gave up (gave up)')
    expect(line({ type: 'auto_retry_end', success: false })).toBe('API retry gave up')
  })

  // The three summarization events state a stall in the SUMMARY that compaction
  // writes, which is a different thing from an API stall and reads differently.
  it('draws the summarization retries under their own scope', () => {
    expect(line({ type: 'summarization_retry_scheduled', attempt: 1, maxAttempts: 3, delayMs: 2000, errorMessage: 'overloaded' }))
      .toBe('Summary retry 1/3 in 2s (overloaded)')
    expect(line({ type: 'summarization_retry_attempt_start', source: 'compaction' }))
      .toBe('Retrying the compaction summary')
    expect(line({ type: 'summarization_retry_finished' })).toBe('Summary retry finished')
  })
})

describe('describePiNotification', () => {
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

  // The structured families left this function when their wording moved to the
  // shared formatter. A second wording here would be a second source for one row.
  it('returns null for the compaction and retry families it no longer owns', () => {
    expect(describePiNotification({ type: 'compaction_start' })).toBeNull()
    expect(describePiNotification({ type: 'compaction_end' })).toBeNull()
    expect(describePiNotification({ type: 'auto_retry_start' })).toBeNull()
    expect(describePiNotification({ type: 'auto_retry_end' })).toBeNull()
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
    expect(renderPiText(msg)).toBe('API retry 2 succeeded')
  })

  it('renders nothing for a shape neither Pi nor the shared switch owns', () => {
    expect(renderPiText({ type: 'totally_unknown_pi_event' })).toBe('')
  })
})

describe('piNotificationEntry', () => {
  // The sole Pi notification seam. It produces STRUCTURED entries, so the shared
  // formatter decides the wording and the shared renderer decides the layout.
  it('maps compaction_start to a compaction entry that is still running', () => {
    expect(piNotificationEntry({ type: 'compaction_start', reason: 'manual' }))
      .toEqual([{ kind: 'compaction', phase: 'start' }])
  })

  it('maps a completed compaction_end to a compaction entry with its detail', () => {
    expect(piNotificationEntry({ type: 'compaction_end', reason: 'threshold', result: { tokensBefore: 12345 } }))
      .toEqual([{ kind: 'compaction', phase: 'end', detail: { trigger: 'threshold', pre: 12345 } }])
  })

  it('maps an aborted compaction_end to a compaction that produced no boundary', () => {
    expect(piNotificationEntry({ type: 'compaction_end', aborted: true, reason: 'threshold' }))
      .toEqual([{ kind: 'compaction', phase: 'end', error: 'aborted' }])
  })

  it('maps a retry to a retry entry under the API scope', () => {
    expect(piNotificationEntry({ type: 'auto_retry_end', success: true, attempt: 2 }))
      .toEqual([{ kind: 'retry', scope: 'api', attempt: 2, willRetry: undefined, error: undefined, succeeded: true }])
  })

  // A LeapMux-authored type never reaches a plugin: the shared extractor answers for
  // it first, so returning nothing here is the correct answer and not a fall-through.
  it('states nothing for a type LeapMux writes itself', () => {
    expect(piNotificationEntry({ type: 'settings_changed' })).toEqual([])
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
    expect(text).toContain('API retry 1/3')
    expect(text).toContain('Context compacted (threshold, 12.3k)')
  })

  it('renders every boundary in a wrapper of two compaction_end events', () => {
    const el = renderThreadElement([
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
