import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { elementText, renderDivider } from '../../messageRenderTestUtils'
import { renderNotificationThread } from '../../notificationRenderers'
import { renderResultDivider } from '../../resultDividerRenderers'

// Side-effect import to register the Claude plugin so the shared
// renderNotificationThread / renderResultDivider consult Claude's
// notificationThreadEntry / resultDivider hooks (mirroring production).
import './plugin'

describe('claude rate-limit notifications', () => {
  // rate_limit_event renders through claudeNotificationThreadEntry; classify only
  // routes a non-allowed status here, so it requires the Claude provider pre-pass.
  it('renders a generic "Rate limit update" for a malformed (non-object) payload', () => {
    // The deleted standalone renderer had this fallback; the entry path preserves
    // it so a malformed payload still surfaces a line instead of vanishing.
    const el = renderNotificationThread([{ type: 'rate_limit_event', rate_limit_info: 'oops' }], AgentProvider.CLAUDE_CODE)
    expect(elementText(el)).toBe('Rate limit update')
  })
})

// ---------------------------------------------------------------------------
// result_divider (Claude, via the shared renderResultDivider + claudeResultDivider hook)
// ---------------------------------------------------------------------------

/** Render a Claude result message through the shared divider and return trimmed text. */
function renderResultText(parsed: Record<string, unknown>): string {
  return renderDivider(parsed, AgentProvider.CLAUDE_CODE).text
}

/** Check if the result is rendered with danger color (error style). */
function isRenderedAsError(parsed: Record<string, unknown>): boolean {
  return renderDivider(parsed, AgentProvider.CLAUDE_CODE).isError
}

describe('result_divider: Claude', () => {
  it('returns null for non-result messages', () => {
    expect(renderResultDivider({ type: 'other' }, AgentProvider.CLAUDE_CODE)).toBeNull()
  })

  it('renders is_error=true as error', () => {
    const parsed = { type: 'result', is_error: true, result: 'Something went wrong' }
    expect(isRenderedAsError(parsed)).toBe(true)
    expect(renderResultText(parsed)).toBe('Turn failed — Something went wrong')
  })

  it('renders a zero-turn unknown-command result (is_error:false) as a plain divider, not a danger dump', () => {
    // Claude Code reports unknown slash commands as is_error:false results that
    // echo their already-shown message. Trust is_error: show the turn end rather
    // than a red dump of the result text. (The renderer ignores stop_reason /
    // num_turns now, so the fixture omits them.)
    const parsed = {
      type: 'result',
      is_error: false,
      subtype: 'success',
      result: 'Unknown command: /non-existent-skill',
      duration_ms: 24,
    }
    expect(isRenderedAsError(parsed)).toBe(false)
    const text = renderResultText(parsed)
    expect(text).toBe('Turn ended (24ms)')
    expect(text).not.toContain('Unknown command')
  })

  it('renders the /usage subscription result as a plain divider, not a danger dump', () => {
    const parsed = {
      type: 'result',
      is_error: false,
      subtype: 'success',
      result: 'You are currently using your subscription to power your Claude Code usage',
      duration_ms: 3,
    }
    expect(isRenderedAsError(parsed)).toBe(false)
    const text = renderResultText(parsed)
    expect(text).toBe('Turn ended (3ms)')
    expect(text).not.toContain('subscription')
  })

  it('renders a success result as a plain turn-end divider, discarding its raw result text', () => {
    const parsed = {
      type: 'result',
      is_error: false,
      subtype: 'success',
      result: '## Context Usage\n\nSome output...',
      duration_ms: 1095,
    }
    expect(isRenderedAsError(parsed)).toBe(false)
    const text = renderResultText(parsed)
    expect(text).toBe('Turn ended (1.1s)')
    expect(text).not.toContain('Context Usage')
  })

  it('renders a non-error result with an absent subtype as a plain divider, not its raw text', () => {
    // A non-error result that omits `subtype` must be treated as success-like
    // (mirroring the error branch's `subtype && ...` guard), so it collapses to
    // the turn end rather than leaking the raw echo text into the label.
    const parsed = {
      type: 'result',
      is_error: false,
      result: 'You are currently using your subscription to power your Claude Code usage',
      duration_ms: 7,
    }
    expect(isRenderedAsError(parsed)).toBe(false)
    const text = renderResultText(parsed)
    expect(text).toBe('Turn ended (7ms)')
    expect(text).not.toContain('subscription')
  })

  it('renders a non-error success result with a missing duration_ms as "Turn ended"', () => {
    // A missing duration_ms has no duration to state, so the label carries none
    // rather than a fake zero.
    const parsed = { type: 'result', is_error: false, subtype: 'success', result: 'done' }
    expect(isRenderedAsError(parsed)).toBe(false)
    expect(renderResultText(parsed)).toBe('Turn ended')
  })

  it('renders a non-error success result with a real zero duration_ms as "Turn ended (0ms)"', () => {
    // A genuine zero is distinct from missing — an instant turn states "(0ms)".
    const parsed = { type: 'result', is_error: false, subtype: 'success', result: 'done', duration_ms: 0 }
    expect(isRenderedAsError(parsed)).toBe(false)
    expect(renderResultText(parsed)).toBe('Turn ended (0ms)')
  })

  it('renders a cancelled result with a missing duration_ms as a bare interruption', () => {
    // No duration to state, so the interruption stands alone.
    const parsed = { type: 'result', is_error: false, subtype: 'cancelled', result: 'Cancelled' }
    expect(isRenderedAsError(parsed)).toBe(false)
    expect(renderResultText(parsed)).toBe('Turn interrupted')
  })

  it('renders a cancelled result with a real zero duration_ms as "Turn interrupted (0ms)"', () => {
    // A real zero is kept, exactly as it is for a turn that ended normally.
    const parsed = { type: 'result', is_error: false, subtype: 'cancelled', result: 'Cancelled', duration_ms: 0 }
    expect(isRenderedAsError(parsed)).toBe(false)
    expect(renderResultText(parsed)).toBe('Turn interrupted (0ms)')
  })

  it('renders success subtype with duration', () => {
    const parsed = { type: 'result', subtype: 'success', stop_reason: 'end_turn', result: 'done', duration_ms: 5000 }
    expect(isRenderedAsError(parsed)).toBe(false)
    expect(renderResultText(parsed)).toBe('Turn ended (5.0s)')
  })

  it('renders a cancelled subtype as an interruption with its duration', () => {
    const parsed = { type: 'result', subtype: 'cancelled', stop_reason: 'end_turn', result: 'Cancelled', duration_ms: 2000 }
    expect(isRenderedAsError(parsed)).toBe(false)
    expect(renderResultText(parsed)).toBe('Turn interrupted (2.0s)')
  })

  // The command-line interface marks its own cancellation with `is_error: true`, the same
  // shape it uses for a genuine failure. The cancelled test used to live past that branch,
  // so a stop the interface reported drew "Turn failed — Cancelled" in the danger color.
  it('renders a cancelled subtype as an interruption even when is_error is set', () => {
    const parsed = { type: 'result', is_error: true, subtype: 'cancelled', result: 'Cancelled', errors: ['Error: Request was aborted.'], duration_ms: 2000 }
    expect(isRenderedAsError(parsed)).toBe(false)
    expect(renderResultText(parsed)).toBe('Turn interrupted (2.0s)')
  })

  it('renders error with subtype as a shared turn-failed divider plus detail', () => {
    const parsed = {
      type: 'result',
      is_error: true,
      subtype: 'error_during_execution',
      errors: ['[ede_diagnostic] result_type=user', 'Error: Request was aborted.'],
      duration_ms: 28563,
    }
    expect(isRenderedAsError(parsed)).toBe(true)
    const text = renderResultText(parsed)
    expect(text).toContain('Turn failed (29s) — Error during execution')
    expect(text).toContain('[ede_diagnostic] result_type=user')
    expect(text).toContain('Error: Request was aborted.')
  })

  // The command-line interface reports a turn the user stopped with the same error
  // subtype it uses for a genuine failure, and its own diagnostics ride in `errors`.
  // LeapMux asked for the stop, so its completion column decides the words.
  it('states the interruption rather than the runtime error subtype', () => {
    const parsed = {
      type: 'result',
      is_error: true,
      subtype: 'error_during_execution',
      errors: ['[ede_diagnostic] result_type=user last_content_type=n/a stop_reason=null'],
      duration_ms: 12000,
    }
    const divider = renderDivider(parsed, AgentProvider.CLAUDE_CODE, MessageCompletion.INTERRUPTED)
    expect(divider.text).toBe('Turn interrupted (12s)')
    expect(divider.isError).toBe(false)
  })

  // Without that column the frame is all there is, so a real failure still reads as
  // one.
  it('keeps the runtime error when no completion states an interruption', () => {
    const parsed = {
      type: 'result',
      is_error: true,
      subtype: 'error_during_execution',
      errors: ['Error: Request was aborted.'],
      duration_ms: 12000,
    }
    const divider = renderDivider(parsed, AgentProvider.CLAUDE_CODE)
    expect(divider.text).toContain('Turn failed (12s) — Error during execution')
    expect(divider.isError).toBe(true)
  })

  it('renders error with subtype but no errors array shows subtype only', () => {
    const parsed = {
      type: 'result',
      is_error: true,
      subtype: 'error_during_execution',
      duration_ms: 5000,
    }
    const text = renderResultText(parsed)
    expect(text).toBe('Turn failed (5.0s) — Error during execution')
  })

  it('renders error without subtype as inline error (legacy behavior)', () => {
    const parsed = { type: 'result', is_error: true, result: 'Something went wrong', duration_ms: 100 }
    const text = renderResultText(parsed)
    expect(text).toBe('Turn failed (100ms) — Something went wrong')
    expect(text).not.toContain('\n')
  })

  it('renders the zero-turn /context result as a plain divider, not a danger dump', () => {
    // The `/context` table is already shown as an assistant bubble above, so the
    // redundant result envelope renders as a normal "Took Xs" turn-end divider
    // rather than dumping its table in danger red.
    const parsed = {
      type: 'result',
      subtype: 'success',
      is_error: false,
      result: '## Context Usage\n\n**Model:** claude-opus-4-8[1m]\n',
      duration_ms: 2062,
    }
    expect(isRenderedAsError(parsed)).toBe(false)
    const text = renderResultText(parsed)
    expect(text).toBe('Turn ended (2.1s)')
    expect(text).not.toContain('Context Usage')
  })

  it('still renders a genuinely failed result red even when its text starts with the context-usage header', () => {
    const parsed = { type: 'result', is_error: true, result: '## Context Usage\nboom', duration_ms: 5 }
    expect(isRenderedAsError(parsed)).toBe(true)
  })
})
