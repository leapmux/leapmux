import { describe, expect, it } from 'vitest'
import { CLAUDE_HIDDEN_SYSTEM_SUBTYPE, CLAUDE_SYSTEM_SUBTYPE } from '~/generated/contracts/claude-protocol'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { elementText, renderDivider, renderThreadElement } from '~/test-support/messageRenderProbes'
import { claudeNotificationEntry, claudeReadsSystemSubtype } from './notification'

// Side-effect import to register the Claude plugin so the shared
// renderNotificationThread and the shared row extraction consult Claude's
// notificationThreadEntry / resultDivider hooks (mirroring production).
import '../plugin'

describe('claude rate-limit notifications', () => {
  // rate_limit_event renders through claudeNotificationThreadEntry; classify only
  // routes a non-allowed status here, so it requires the Claude provider pre-pass.
  it('renders a generic "Rate limit update" for a malformed (non-object) payload', () => {
    // The deleted standalone renderer had this fallback; the entry path preserves
    // it so a malformed payload still surfaces a line instead of vanishing.
    const el = renderThreadElement([
      { type: 'rate_limit_event', rate_limit_info: 'oops' },
    ], AgentProvider.CLAUDE_CODE)
    expect(elementText(el)).toBe('Rate limit update')
  })
})

// ---------------------------------------------------------------------------
// result_divider (Claude, through the shared row extraction + claudeResultDivider hook)
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
  it('draws nothing for non-result messages', () => {
    expect(renderResultText({ type: 'other' })).toBe('')
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
    const parsed = {
      type: 'result',
      is_error: false,
      subtype: 'success',
      result: 'done',
    }
    expect(isRenderedAsError(parsed)).toBe(false)
    expect(renderResultText(parsed)).toBe('Turn ended')
  })

  it('renders a non-error success result with a real zero duration_ms as "Turn ended (0ms)"', () => {
    // A genuine zero is distinct from missing — an instant turn states "(0ms)".
    const parsed = {
      type: 'result',
      is_error: false,
      subtype: 'success',
      result: 'done',
      duration_ms: 0,
    }
    expect(isRenderedAsError(parsed)).toBe(false)
    expect(renderResultText(parsed)).toBe('Turn ended (0ms)')
  })

  it('renders a cancelled result with a missing duration_ms as a bare interruption', () => {
    // No duration to state, so the interruption stands alone.
    const parsed = {
      type: 'result',
      is_error: false,
      subtype: 'cancelled',
      result: 'Cancelled',
    }
    expect(isRenderedAsError(parsed)).toBe(false)
    expect(renderResultText(parsed)).toBe('Turn interrupted')
  })

  it('renders a cancelled result with a real zero duration_ms as "Turn interrupted (0ms)"', () => {
    // A real zero is kept, exactly as it is for a turn that ended normally.
    const parsed = {
      type: 'result',
      is_error: false,
      subtype: 'cancelled',
      result: 'Cancelled',
      duration_ms: 0,
    }
    expect(isRenderedAsError(parsed)).toBe(false)
    expect(renderResultText(parsed)).toBe('Turn interrupted (0ms)')
  })

  it('renders success subtype with duration', () => {
    const parsed = {
      type: 'result',
      subtype: 'success',
      stop_reason: 'end_turn',
      result: 'done',
      duration_ms: 5000,
    }
    expect(isRenderedAsError(parsed)).toBe(false)
    expect(renderResultText(parsed)).toBe('Turn ended (5.0s)')
  })

  it('renders a cancelled subtype as an interruption with its duration', () => {
    const parsed = {
      type: 'result',
      subtype: 'cancelled',
      stop_reason: 'end_turn',
      result: 'Cancelled',
      duration_ms: 2000,
    }
    expect(isRenderedAsError(parsed)).toBe(false)
    expect(renderResultText(parsed)).toBe('Turn interrupted (2.0s)')
  })

  // The command-line interface marks its own cancellation with `is_error: true`, the same
  // shape it uses for a genuine failure. The cancelled test used to live past that branch,
  // so a stop the interface reported drew "Turn failed — Cancelled" in the danger color.
  it('renders a cancelled subtype as an interruption even when is_error is set', () => {
    const parsed = {
      type: 'result',
      is_error: true,
      subtype: 'cancelled',
      result: 'Cancelled',
      errors: ['Error: Request was aborted.'],
      duration_ms: 2000,
    }
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
    const parsed = {
      type: 'result',
      is_error: true,
      result: 'Something went wrong',
      duration_ms: 100,
    }
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
    const parsed = {
      type: 'result',
      is_error: true,
      result: '## Context Usage\nboom',
      duration_ms: 5,
    }
    expect(isRenderedAsError(parsed)).toBe(true)
  })
})

// ---------------------------------------------------------------------------
// claudeNotificationEntry: the text of every frame that draws a row.
// ---------------------------------------------------------------------------

describe('claudeNotificationEntry', () => {
  it('reads a notification frame and leads an error color with "Error:"', () => {
    expect(claudeNotificationEntry({
      type: 'system',
      subtype: 'notification',
      key: 'compact-error',
      text: 'Error compacting conversation',
      priority: 'high',
    }))
      .toEqual([
        { kind: 'text', text: 'Error compacting conversation' },
      ])
    // `color` is optional on the wire; "error" is the one value the interface colours red.
    expect(claudeNotificationEntry({
      type: 'system',
      subtype: 'notification',
      key: 'compact-error',
      text: 'Error compacting conversation',
      priority: 'high',
      color: 'error',
    }))
      .toEqual([
        { kind: 'text', text: 'Error: Error compacting conversation' },
      ])
  })

  it('states nothing for an error notification with no text', () => {
    // The prefix alone states no error, so the frame gets no "Error: " line.
    expect(claudeNotificationEntry({
      type: 'system',
      subtype: 'notification',
      key: 'compact-error',
      text: '',
      priority: 'high',
      color: 'error',
    }))
      .toEqual([])
    expect(claudeNotificationEntry({
      type: 'system',
      subtype: 'notification',
      key: 'compact-error',
      priority: 'high',
      color: 'error',
    }))
      .toEqual([])
  })

  it('reads an informational frame', () => {
    expect(claudeNotificationEntry({
      type: 'system',
      subtype: 'informational',
      content: 'Stop hook failed: boom',
      level: 'warning',
    }))
      .toEqual([
        { kind: 'text', text: 'Stop hook failed: boom' },
      ])
  })

  it('reads a memory_recall frame as the count of recalled memories', () => {
    // Claude Code 2.1.300 writes "Recalled N memories" for every scope. It adds
    // "local" only when a feature flag is on, and that build always sets the
    // flag off.
    expect(claudeNotificationEntry({
      type: 'system',
      subtype: 'memory_recall',
      mode: 'select',
      memories: [
        { path: '/m/a.md', scope: 'personal' },
        { path: '/m/b.md', scope: 'personal' },
      ],
    }))
      .toEqual([
        { kind: 'text', text: 'Recalled 2 memories' },
      ])
    expect(claudeNotificationEntry({
      type: 'system',
      subtype: 'memory_recall',
      mode: 'select',
      memories: [
        { path: '/m/a.md', scope: 'personal' },
        { path: 'https://corp.example/mem/b', scope: 'organization' },
      ],
    }))
      .toEqual([
        { kind: 'text', text: 'Recalled 2 memories' },
      ])
    expect(claudeNotificationEntry({
      type: 'system',
      subtype: 'memory_recall',
      mode: 'select',
      memories: [
        { path: '/m/a.md', scope: 'personal' },
      ],
    }))
      .toEqual([
        { kind: 'text', text: 'Recalled 1 memory' },
      ])
  })

  it('states nothing for a memory_recall frame that recalls no memory', () => {
    // "Recalled 0 memories" states nothing that happened.
    expect(claudeNotificationEntry({
      type: 'system',
      subtype: 'memory_recall',
      mode: 'select',
      memories: [],
    }))
      .toEqual([])
    // `memories` that is absent or not a list reads as no memory.
    expect(claudeNotificationEntry({ type: 'system', subtype: 'memory_recall', mode: 'select' }))
      .toEqual([])
    expect(claudeNotificationEntry({
      type: 'system',
      subtype: 'memory_recall',
      mode: 'select',
      memories: 'a.md',
    }))
      .toEqual([])
    // An element that is not an object is not a memory.
    expect(claudeNotificationEntry({
      type: 'system',
      subtype: 'memory_recall',
      mode: 'select',
      memories: ['a.md', null],
    }))
      .toEqual([])
  })

  it('reads a failing hook_response and omits a missing exit code', () => {
    expect(claudeNotificationEntry({
      type: 'system',
      subtype: 'hook_response',
      hook_id: 'h1',
      hook_name: 'setup.sh',
      hook_event: 'SessionStart',
      output: '',
      stdout: '',
      stderr: 'boom',
      exit_code: 2,
      outcome: 'error',
    }))
      .toEqual([
        {
          kind: 'hook',
          outcome: 'failed',
          event: 'SessionStart',
          name: 'setup.sh',
          exitCode: 2,
        },
      ])
    // `exit_code` is optional on the wire.
    expect(claudeNotificationEntry({
      type: 'system',
      subtype: 'hook_response',
      hook_id: 'h1',
      hook_name: 'setup.sh',
      hook_event: 'SessionStart',
      output: '',
      stdout: '',
      stderr: 'boom',
      outcome: 'error',
    }))
      .toEqual([
        {
          kind: 'hook',
          outcome: 'failed',
          event: 'SessionStart',
          name: 'setup.sh',
        },
      ])
    expect(claudeNotificationEntry({
      type: 'system',
      subtype: 'hook_response',
      hook_id: 'h1',
      hook_name: 'setup.sh',
      hook_event: 'SessionStart',
      output: '',
      stdout: '',
      stderr: '',
      exit_code: 0,
      outcome: 'success',
    }))
      .toEqual([])
    expect(claudeNotificationEntry({
      type: 'system',
      subtype: 'hook_response',
      hook_id: 'h1',
      hook_name: 'setup.sh',
      hook_event: 'SessionStart',
      outcome: 'cancelled',
    }))
      .toEqual([])
  })

  it('reads a failing hook_response that states no event or no name', () => {
    // The row never shows an empty pair of brackets or a double space.
    expect(claudeNotificationEntry({
      type: 'system',
      subtype: 'hook_response',
      hook_id: 'h1',
      hook_event: 'SessionStart',
      outcome: 'error',
      exit_code: 1,
    }))
      .toEqual([
        {
          kind: 'hook',
          outcome: 'failed',
          event: 'SessionStart',
          exitCode: 1,
        },
      ])
    expect(claudeNotificationEntry({
      type: 'system',
      subtype: 'hook_response',
      hook_id: 'h1',
      hook_name: 'setup.sh',
      outcome: 'error',
    }))
      .toEqual([
        { kind: 'hook', outcome: 'failed', name: 'setup.sh' },
      ])
    expect(claudeNotificationEntry({
      type: 'system',
      subtype: 'hook_response',
      hook_id: 'h1',
      outcome: 'error',
    }))
      .toEqual([
        { kind: 'hook', outcome: 'failed' },
      ])
  })

  it('reads ui_log and ui_toast frames', () => {
    expect(claudeNotificationEntry({
      type: 'system',
      subtype: 'ui_log',
      plugin: 'my-plugin',
      text: 'hello',
    }))
      .toEqual([
        { kind: 'text', text: 'my-plugin: hello' },
      ])
    expect(claudeNotificationEntry({
      type: 'system',
      subtype: 'ui_toast',
      plugin: 'my-plugin',
      text: 'hello',
      timeout_ms: 3000,
    }))
      .toEqual([
        { kind: 'text', text: 'my-plugin: hello' },
      ])
    // A frame that states no plugin shows its text without an empty prefix.
    expect(claudeNotificationEntry({ type: 'system', subtype: 'ui_log', text: 'hello' }))
      .toEqual([
        { kind: 'text', text: 'hello' },
      ])
  })

  it('reads each model fallback frame', () => {
    for (const subtype of ['model_fallback', 'model_refusal_fallback', 'model_consent_fallback']) {
      expect(claudeNotificationEntry({
        type: 'system',
        subtype,
        original_model: 'claude-opus-5-5',
        fallback_model: 'claude-sonnet-5-5',
        content: 'Switched to claude-sonnet-5-5',
      }), subtype)
        .toEqual([
          { kind: 'text', text: 'Switched to claude-sonnet-5-5' },
        ])
    }
  })

  it('reads an api_retry frame, with only the optional fields that it states', () => {
    expect(claudeNotificationEntry({
      type: 'system',
      subtype: 'api_retry',
      attempt: 2,
      max_retries: 5,
      retry_delay_ms: 4000,
      error_status: 529,
      error: 'overloaded',
    }))
      .toEqual([
        {
          kind: 'retry',
          scope: 'api',
          attempt: 2,
          maxAttempts: 5,
          delayMs: 4000,
          error: '529 overloaded',
        },
      ])
    expect(claudeNotificationEntry({ type: 'system', subtype: 'api_retry' }))
      .toEqual([
        { kind: 'retry', scope: 'api' },
      ])
  })

  it('reads a permission_denied frame with and without its tool and reason', () => {
    expect(claudeNotificationEntry({
      type: 'system',
      subtype: 'permission_denied',
      tool_name: 'Bash',
      decision_reason: 'the command removes a directory',
    }))
      .toEqual([
        { kind: 'text', text: 'Permission denied: Bash - the command removes a directory' },
      ])
    expect(claudeNotificationEntry({ type: 'system', subtype: 'permission_denied', message: 'no reason given' }))
      .toEqual([
        { kind: 'text', text: 'Permission denied - no reason given' },
      ])
    expect(claudeNotificationEntry({ type: 'system', subtype: 'permission_denied' }))
      .toEqual([
        { kind: 'text', text: 'Permission denied' },
      ])
  })

  it('reads a failed final status as a failed compaction with its error', () => {
    expect(claudeNotificationEntry({
      type: 'system',
      subtype: 'status',
      status: null,
      compact_result: 'failed',
      compact_error: 'too_few_groups',
    }))
      .toEqual([
        {
          kind: 'compaction',
          phase: 'end',
          outcome: 'failed',
          error: 'too_few_groups',
        },
      ])
    expect(claudeNotificationEntry({ type: 'system', subtype: 'status', compact_result: 'failed' }))
      .toEqual([
        { kind: 'compaction', phase: 'end', outcome: 'failed' },
      ])
  })

  it('reads no entry for a compacting status', () => {
    expect(claudeNotificationEntry({ type: 'system', subtype: 'status', status: 'compacting' })).toEqual([])
  })

  it('validates retry counters through the neutral constructor', () => {
    expect(claudeNotificationEntry({
      type: 'system',
      subtype: 'api_retry',
      attempt: -1,
      max_retries: 1.5,
      retry_delay_ms: 0.5,
      error: '  temporary error  ',
    }))
      .toEqual([
        {
          kind: 'retry',
          scope: 'api',
          delayMs: 0.5,
          error: 'temporary error',
        },
      ])
  })

  it('reads the stop-hook-error notification as a failed Stop hook', () => {
    const frame = {
      type: 'system',
      subtype: 'notification',
      key: 'stop-hook-error',
      text: 'Stop hook error occurred · ctrl+o to see',
    }
    expect(claudeNotificationEntry(frame)).toEqual([
      { kind: 'hook', outcome: 'failed', event: 'Stop' },
    ])
    expect(elementText(renderThreadElement([frame], AgentProvider.CLAUDE_CODE))).toBe('Hook Stop failed')
  })

  // A frame that carries no words gets no entry: an empty notification is worse
  // than none. The classifier then hides the row of a subtype that this reader
  // knows (claudeReadsSystemSubtype). The `post_turn_summary` frame states its
  // summary in fields that no reader shows, and the hidden-subtype table hides
  // its row before this reader runs.
  it('states nothing for a frame whose words are empty', () => {
    expect(claudeNotificationEntry({ type: 'system', subtype: 'notification', text: '' })).toEqual([])
    expect(claudeNotificationEntry({ type: 'system', subtype: 'informational', content: '' })).toEqual([])
    expect(claudeNotificationEntry({
      type: 'system',
      subtype: 'ui_log',
      plugin: 'my-plugin',
      text: '',
    })).toEqual([])
    expect(claudeNotificationEntry({ type: 'system', subtype: 'model_fallback', content: '' })).toEqual([])
    expect(claudeNotificationEntry({ type: 'system', subtype: 'post_turn_summary', summary: 'said things' })).toEqual([])
  })

  it('states nothing for a frame of an unknown subtype or type', () => {
    expect(claudeNotificationEntry({ type: 'system', subtype: 'some_future_event', text: 'hello' })).toEqual([])
    expect(claudeNotificationEntry({ type: 'system' })).toEqual([])
    expect(claudeNotificationEntry({ type: 'some_future_type', subtype: 'notification', text: 'hello' })).toEqual([])
  })
})

describe('claudeReadsSystemSubtype', () => {
  it('knows each subtype that draws its own words', () => {
    for (const subtype of [
      ...Object.values(CLAUDE_SYSTEM_SUBTYPE),
      'memory_recall',
      'ui_log',
      'ui_toast',
      'model_fallback',
    ]) {
      expect(claudeReadsSystemSubtype(subtype), subtype).toBe(true)
    }
  })

  it('does not know an unknown subtype or a key of the object prototype', () => {
    // A prototype key from the wire must not select an inherited property.
    for (const subtype of [
      'some_future_event',
      '',
      'api_error',
      'toString',
      'constructor',
      '__proto__',
    ])
      expect(claudeReadsSystemSubtype(subtype), subtype).toBe(false)
  })

  it('knows no subtype that the hidden table lists', () => {
    // A subtype is read or hidden, never both: a hidden subtype never reaches
    // the reader, so a reader for it would be dead code.
    for (const subtype of Object.values(CLAUDE_HIDDEN_SYSTEM_SUBTYPE))
      expect(claudeReadsSystemSubtype(subtype), subtype).toBe(false)
  })
})
