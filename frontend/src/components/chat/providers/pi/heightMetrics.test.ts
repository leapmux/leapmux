import type { RowUiState } from '../../chatHeightEstimator'
import type { MessageCategory } from '../../messageClassification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { providerFor } from '../registry'
import './plugin'

const RESULT: MessageCategory = { kind: 'tool_result' }
// Pi ignores the row UI state; pass a collapsed default so the tests stay 3-arg.
const NO_UI_STATE: RowUiState = { collapsed: false, expanded: false, toolBodyExpanded: false, diffView: 'unified' }
const baseHook = providerFor(AgentProvider.PI)!.heightMetrics!
const hook = (c: MessageCategory, p: ParsedMessageContent, t: ParsedMessageContent | undefined) => baseHook(c, p, t, NO_UI_STATE)

function parsed(parentObject: Record<string, unknown>): ParsedMessageContent {
  return { rawText: '', topLevel: parentObject, parentObject, wrapper: null }
}

/** A Pi tool_execution_end payload (classified `tool_result`). */
function end(over: Record<string, unknown>): ParsedMessageContent {
  return parsed({ type: 'tool_execution_end', toolCallId: 't1', toolName: 'edit', isError: false, ...over })
}

/** A Pi tool_execution_start sibling carrying the original args. */
function start(toolName: string, args: Record<string, unknown>): ParsedMessageContent {
  return parsed({ type: 'tool_execution_start', toolCallId: 't1', toolName, args })
}

/** A Pi `tool_use` category (the start payload classified by the plugin). */
function toolUse(toolName: string, args: Record<string, unknown>): MessageCategory {
  return { kind: 'tool_use', toolName, toolUse: { type: 'tool_execution_start', toolCallId: 't1', toolName, args }, content: [] }
}

describe('pi heightMetrics', () => {
  it('sizes an edit diff from the inline result details.diff (Pi numbered format)', () => {
    const diff = [' 1 first line', '-2 old text', '+2 new text', ' 3 third line'].join('\n')
    const fields = hook(RESULT, end({ result: { details: { diff } } }), undefined)!
    expect(fields).not.toBeNull()
    expect(fields.diffAdded).toBe(1)
    expect(fields.diffRemoved).toBe(1)
  })

  it('falls back to the tool_use-start args when the result has no inline diff (needs the sibling)', () => {
    const sibling = start('edit', { path: 'a.ts', edits: [{ oldText: 'a\nb', newText: 'a\nB' }] })
    const fields = hook(RESULT, end({ toolName: 'edit', result: { text: 'done' } }), sibling)!
    expect(fields).not.toBeNull()
    expect((fields.diffAdded ?? 0) + (fields.diffRemoved ?? 0)).toBeGreaterThan(0)
  })

  it('sizes a write fallback from the start content as an all-added diff', () => {
    const sibling = start('write', { path: 'n.ts', content: 'x\ny\nz' })
    const fields = hook(RESULT, end({ toolName: 'write', result: { text: 'done' } }), sibling)!
    expect(fields.diffAdded).toBe(3)
    expect(fields.diffRemoved).toBe(0)
  })

  it('returns null for a failed edit (renders error text, not a diff)', () => {
    const diff = [' 1 a', '-2 old', '+2 new'].join('\n')
    expect(hook(RESULT, end({ isError: true, result: { details: { diff } } }), undefined)).toBeNull()
  })

  it('returns null for a non-edit/write tool (bash)', () => {
    expect(hook(RESULT, end({ toolName: 'bash', result: { text: 'output' } }), undefined)).toBeNull()
  })

  it('returns null for a non-tool_result category', () => {
    expect(hook({ kind: 'assistant_text' }, end({}), undefined)).toBeNull()
  })

  // A Pi Bash tool_use renders its FULL command (PiBashRenderer, alwaysVisible). Pi
  // stores it under args.command and classifies Bash lowercase, so the generic
  // estimator can't reach it -- the hook must size it or a multi-line command
  // estimates as a bare header (an off-screen under-estimate that drifts scroll).
  it('sizes a multi-line Bash tool_use from its full command body', () => {
    const command = 'echo one\necho two\necho three'
    const fields = hook(toolUse('bash', { command }), parsed({}), undefined)!
    expect(fields).not.toBeNull()
    expect(fields.textLength).toBe(command.length)
    expect(fields.logicalLineCount).toBe(3)
    // Per-hard-line lengths MUST be fed so estimateToolUseHeader sums each line's wrap
    // (the pre-wrap summary block); without them it falls back to the flat total-wrap
    // model and under-counts a multi-line command.
    expect(fields.lineLengths).toEqual([8, 8, 10]) // 'echo one', 'echo two', 'echo three'
  })

  it('returns null for a non-bash tool_use (routes to generic sizing)', () => {
    expect(hook(toolUse('read', { path: 'a.ts' }), parsed({}), undefined)).toBeNull()
  })

  it('returns null for a bash tool_use with no command', () => {
    expect(hook(toolUse('bash', {}), parsed({}), undefined)).toBeNull()
  })
})
