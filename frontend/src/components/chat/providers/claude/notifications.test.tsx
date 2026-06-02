import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
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
    expect(renderResultText(parsed)).toBe('Something went wrong')
  })

  it('renders a zero-turn unknown-command result (is_error:false) as a plain divider, not a danger dump', () => {
    // Claude Code reports unknown slash commands as is_error:false results that
    // echo their already-shown message. Trust is_error: show "Took Xs" rather
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
    expect(text).toBe('Took 24ms')
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
    expect(text).toBe('Took 3ms')
    expect(text).not.toContain('subscription')
  })

  it('renders a success result as a plain "Took Xs" divider, discarding its raw result text', () => {
    const parsed = {
      type: 'result',
      is_error: false,
      subtype: 'success',
      result: '## Context Usage\n\nSome output...',
      duration_ms: 1095,
    }
    expect(isRenderedAsError(parsed)).toBe(false)
    const text = renderResultText(parsed)
    expect(text).toBe('Took 1.1s')
    expect(text).not.toContain('Context Usage')
  })

  it('renders a non-error result with an absent subtype as a plain divider, not its raw text', () => {
    // A non-error result that omits `subtype` must be treated as success-like
    // (mirroring the error branch's `subtype && ...` guard), so it collapses to
    // "Took Xs" rather than leaking the raw echo text into the label.
    const parsed = {
      type: 'result',
      is_error: false,
      result: 'You are currently using your subscription to power your Claude Code usage',
      duration_ms: 7,
    }
    expect(isRenderedAsError(parsed)).toBe(false)
    const text = renderResultText(parsed)
    expect(text).toBe('Took 7ms')
    expect(text).not.toContain('subscription')
  })

  it('renders a non-error success result with a missing duration_ms as "Turn ended"', () => {
    // A missing duration_ms has no meaningful "Took" value, so the duration-only
    // divider falls back to "Turn ended" instead of a fake "Took 0ms".
    const parsed = { type: 'result', is_error: false, subtype: 'success', result: 'done' }
    expect(isRenderedAsError(parsed)).toBe(false)
    expect(renderResultText(parsed)).toBe('Turn ended')
  })

  it('renders a non-error success result with a real zero duration_ms as "Took 0ms"', () => {
    // A genuine zero is distinct from missing — an instant turn is "Took 0ms".
    const parsed = { type: 'result', is_error: false, subtype: 'success', result: 'done', duration_ms: 0 }
    expect(isRenderedAsError(parsed)).toBe(false)
    expect(renderResultText(parsed)).toBe('Took 0ms')
  })

  it('renders a non-success non-error result with a missing duration_ms as just its text', () => {
    // displayText present + no duration -> the suffix is dropped, not "(0ms)".
    const parsed = { type: 'result', is_error: false, subtype: 'cancelled', result: 'Cancelled' }
    expect(isRenderedAsError(parsed)).toBe(false)
    expect(renderResultText(parsed)).toBe('Cancelled')
  })

  it('renders a non-success non-error result with a real zero duration_ms as "<text> (0ms)"', () => {
    // displayText present + real zero -> the suffix is kept, mirroring "Took 0ms".
    const parsed = { type: 'result', is_error: false, subtype: 'cancelled', result: 'Cancelled', duration_ms: 0 }
    expect(isRenderedAsError(parsed)).toBe(false)
    expect(renderResultText(parsed)).toBe('Cancelled (0ms)')
  })

  it('renders success subtype with duration', () => {
    const parsed = { type: 'result', subtype: 'success', stop_reason: 'end_turn', result: 'done', duration_ms: 5000 }
    expect(isRenderedAsError(parsed)).toBe(false)
    expect(renderResultText(parsed)).toBe('Took 5.0s')
  })

  it('renders non-success subtype with result text and duration', () => {
    const parsed = { type: 'result', subtype: 'cancelled', stop_reason: 'end_turn', result: 'Cancelled', duration_ms: 2000 }
    expect(isRenderedAsError(parsed)).toBe(false)
    expect(renderResultText(parsed)).toBe('Cancelled (2.0s)')
  })

  it('renders error with subtype as humanized divider + detail', () => {
    const parsed = {
      type: 'result',
      is_error: true,
      subtype: 'error_during_execution',
      errors: ['[ede_diagnostic] result_type=user', 'Error: Request was aborted.'],
      duration_ms: 28563,
    }
    expect(isRenderedAsError(parsed)).toBe(true)
    const text = renderResultText(parsed)
    expect(text).toContain('Error during execution (29s)')
    expect(text).toContain('[ede_diagnostic] result_type=user')
    expect(text).toContain('Error: Request was aborted.')
  })

  it('renders error with subtype but no errors array shows subtype only', () => {
    const parsed = {
      type: 'result',
      is_error: true,
      subtype: 'error_during_execution',
      duration_ms: 5000,
    }
    const text = renderResultText(parsed)
    expect(text).toBe('Error during execution (5.0s)')
  })

  it('renders error without subtype as inline error (legacy behavior)', () => {
    const parsed = { type: 'result', is_error: true, result: 'Something went wrong', duration_ms: 100 }
    const text = renderResultText(parsed)
    expect(text).toBe('Something went wrong (100ms)')
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
    expect(text).toBe('Took 2.1s')
    expect(text).not.toContain('Context Usage')
  })

  it('still renders a genuinely failed result red even when its text starts with the context-usage header', () => {
    const parsed = { type: 'result', is_error: true, result: '## Context Usage\nboom', duration_ms: 5 }
    expect(isRenderedAsError(parsed)).toBe(true)
  })
})
