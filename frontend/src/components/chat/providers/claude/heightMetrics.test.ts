import type { RowUiState } from '../../chatHeightEstimator'
import type { MessageCategory } from '../../messageClassification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { providerFor } from '../registry'
import './plugin'

// Claude ignores the row UI state; pass a collapsed default so the tests stay 3-arg.
const NO_UI_STATE: RowUiState = { collapsed: false, expanded: false, toolBodyExpanded: false, diffView: 'unified' }
const baseHook = providerFor(AgentProvider.CLAUDE_CODE)!.heightMetrics!
const hook = (c: MessageCategory, p: ParsedMessageContent, t: ParsedMessageContent | undefined) => baseHook(c, p, t, NO_UI_STATE)
const RESULT: MessageCategory = { kind: 'tool_result' }
const DIVIDER: MessageCategory = { kind: 'result_divider' }

function parsed(over: Partial<ParsedMessageContent>): ParsedMessageContent {
  return { rawText: '', topLevel: null, parentObject: undefined, wrapper: null, ...over }
}

/** A Claude tool_result parsed message carrying a tool_use_result payload. */
function toolResult(tur: Record<string, unknown>, opts: { isError?: boolean } = {}): ParsedMessageContent {
  return parsed({
    parentObject: {
      message: { content: [{ type: 'tool_result', is_error: opts.isError ?? false, content: 'x' }] },
      tool_use_result: tur,
    },
  })
}

describe('claude heightMetrics diff', () => {
  it('sizes an Edit tool_result diff from oldString/newString', () => {
    const fields = hook(RESULT, toolResult({ filePath: 'a.ts', oldString: 'a\nb\nc', newString: 'a\nB\nc' }), undefined)!
    expect((fields.diffUnifiedRows ?? 0)).toBeGreaterThan(0)
    expect((fields.diffAdded ?? 0) + (fields.diffRemoved ?? 0)).toBeGreaterThan(0)
  })

  it('prefers a precomputed structuredPatch', () => {
    const fields = hook(RESULT, toolResult({
      filePath: 'a.ts',
      structuredPatch: [{ oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, lines: ['-old', '+new'] }],
    }), undefined)!
    expect(fields.diffHunkCount).toBe(1)
    expect(fields.diffAdded).toBe(1)
    expect(fields.diffRemoved).toBe(1)
  })

  it('synthesizes a Write-create diff from the result content when no sibling is present', () => {
    const fields = hook(RESULT, toolResult({ type: 'create', filePath: 'new.ts', content: 'line1\nline2\nline3' }), undefined)!
    expect(fields.diffAdded).toBe(3) // all-added new file
    expect(fields.diffRemoved).toBe(0)
  })

  it('uses the paired tool_use input when the result carries no diff (Write-create with sibling)', () => {
    // Result has no structuredPatch/oldString/newString/content; the diff comes
    // from the paired Write tool_use input's `content` (pickFileEditDiff fallback).
    const sibling = parsed({
      parentObject: { type: 'assistant', message: { content: [{ type: 'tool_use', name: 'Write', input: { file_path: 'n.ts', content: 'a\nb' } }] } },
    })
    const fields = hook(RESULT, toolResult({ filePath: 'n.ts' }), sibling)!
    expect(fields.diffAdded).toBe(2)
  })

  it('does not size a FAILED edit as a diff (renders error text)', () => {
    const p = toolResult({ filePath: 'a.ts', oldString: 'a', newString: 'b' }, { isError: true })
    const fields = hook(RESULT, p, undefined)!
    expect(fields.diffUnifiedRows).toBeUndefined()
  })

  it('does not size a non-edit tool result as a diff', () => {
    const fields = hook(RESULT, toolResult({ tool_name: 'Read', filePath: 'a.ts' }), undefined)!
    expect(fields.diffUnifiedRows).toBeUndefined()
  })
})

describe('claude heightMetrics read', () => {
  /** A Claude Read tool_result whose body is the raw `resultContent` string. */
  function readResult(resultContent: string): ParsedMessageContent {
    return parsed({
      parentObject: {
        message: { content: [{ type: 'tool_result', is_error: false, content: resultContent }] },
        tool_use_result: { tool_name: 'Read' },
      },
    })
  }

  it('sizes a read body from the PARSED cat-n lines, excluding a trailing reminder block', () => {
    // Raw content is a 3-line cat-n body PLUS a 3-line <system-reminder> block that
    // parseReadContent strips into a trailing alert (rendered only when expanded). The
    // estimate must count the PARSED body (3), not the raw text (6): the renderer
    // collapses on items().length, so charging the reminder lines trips the collapse
    // gate at the wrong row count and under-estimates the row when it renders in full.
    // Mirrors the ACP read test -- the same fix, applied to the Claude tool_result path.
    const p = readResult('   1\tconst a = 1\n   2\tconst b = 2\n   3\tconst c = 3\n<system-reminder>\nAvoid excessive reads.\n</system-reminder>')
    const fields = hook(RESULT, p, undefined)!
    expect(fields.logicalLineCount).toBe(3) // the 3 cat-n body lines, NOT the 6 raw lines
    expect(fields.bodyMarkdown).toBe(false) // sized mono like the renderer, not markdown
  })

  it('falls through to generic sizing for a non-cat-n read (no parsed lines, no override)', () => {
    // Unstructured prose that doesn't parse as cat-n: claudeReadHeightFields returns
    // null so the generic text-body sizing (buildHeightInput) covers it -- the renderer
    // falls back to the raw content too. The hook must NOT impose a stripped-line count.
    const p = readResult('just some unstructured text\nwith two lines')
    const fields = hook(RESULT, p, undefined)!
    expect(fields.logicalLineCount).toBeUndefined()
  })

  it('does not override a FAILED read (renders full error text, not a collapsed body)', () => {
    // An errored Read renders the full error via the catch-all; claudeCustomResultFields
    // bails on isError before the READ case, so no stripped-line collapse override applies.
    const p = parsed({
      parentObject: {
        message: { content: [{ type: 'tool_result', is_error: true, content: '   1\tx\n<system-reminder>\nr\n</system-reminder>' }] },
        tool_use_result: { tool_name: 'Read' },
      },
    })
    const fields = hook(RESULT, p, undefined)!
    expect(fields.logicalLineCount).toBeUndefined()
  })
})

describe('claude heightMetrics tool_result flags', () => {
  it('flags an Agent sub-agent result as markdown + header', () => {
    const fields = hook(RESULT, toolResult({ agentId: 'abc', content: 'hi' }), undefined)!
    expect(fields.bodyMarkdown).toBe(true)
    expect(fields.hasHeader).toBe(true)
  })

  it('flags a WebFetch / MCP result as markdown (no header)', () => {
    expect(hook(RESULT, toolResult({ tool_name: 'WebFetch' }), undefined)!.bodyMarkdown).toBe(true)
    expect(hook(RESULT, toolResult({ tool_name: 'mcp__srv__tool' }), undefined)!.bodyMarkdown).toBe(true)
  })

  it('flags an interrupted result as having a header', () => {
    expect(hook(RESULT, toolResult({ interrupted: true }), undefined)!.hasHeader).toBe(true)
  })

  it('a plain Read result is neither markdown nor header', () => {
    const fields = hook(RESULT, toolResult({ tool_name: 'Read' }), undefined)!
    expect(fields.bodyMarkdown).toBe(false)
    expect(fields.hasHeader).toBe(false)
  })
})

describe('claude heightMetrics AskUserQuestion result', () => {
  it('sizes by per-question "header: answer" rows, not the raw content', () => {
    const fields = hook(RESULT, toolResult({
      questions: [{ header: 'Fix', question: 'How to fix the bug?' }],
      answers: { 'How to fix the bug?': 'Patch the root cause' },
    }), undefined)!
    expect(fields.askAnswerLineLengths).toEqual(['Fix: Patch the root cause'.length])
  })

  it('falls back to the header key and "Not answered" when the answer is absent', () => {
    const fields = hook(RESULT, toolResult({ questions: [{ header: 'Q1', question: 'pick one' }], answers: {} }), undefined)!
    expect(fields.askAnswerLineLengths).toEqual(['Q1: Not answered'.length])
  })

  it('is not treated as an ask result when there is no questions array', () => {
    const fields = hook(RESULT, toolResult({ tool_name: 'Read', filePath: 'a.ts' }), undefined)!
    expect(fields.askAnswerLineLengths).toBeUndefined()
  })
})

describe('claude heightMetrics custom result renderers', () => {
  it('sizes a ToolSearch matches list uncollapsed (not the empty content string)', () => {
    const fields = hook(RESULT, toolResult({ tool_name: 'ToolSearch', matches: ['Read', 'Glob', 'Grep', 'Bash'] }), undefined)!
    expect(fields.logicalLineCount).toBe(4)
    expect(fields.lineLengths).toHaveLength(4)
    expect(fields.uncollapsed).toBe(true)
    expect(fields.bodyMarkdown).toBe(false)
    // Empty matches -> the "No tools found" placeholder (one row).
    expect(hook(RESULT, toolResult({ tool_name: 'ToolSearch', matches: [] }), undefined)!.logicalLineCount).toBe(1)
  })

  it('emits the WebSearch link count + summary metrics from tool_use_result.results', () => {
    const fields = hook(RESULT, toolResult({
      tool_name: 'WebSearch',
      results: [{ content: [{ title: 'A', url: 'https://a.example' }, { title: 'B', url: 'https://b.example' }] }, 'a final summary'],
    }), undefined)!
    expect(fields.webSearchLinkCount).toBe(2)
    expect(fields.textLength).toBe('a final summary'.length)
    // No links -> falls back to the generic path (no webSearchLinkCount).
    expect(hook(RESULT, toolResult({ tool_name: 'WebSearch', results: [] }), undefined)!.webSearchLinkCount).toBeUndefined()
  })

  it('sizes a WebFetch markdown body from tool_use_result.result + a summary line', () => {
    const fields = hook(RESULT, toolResult({ tool_name: 'WebFetch', code: 200, codeText: 'OK', result: 'l1\nl2\nl3\nl4' }), undefined)!
    expect(fields.summaryLineCount).toBe(1)
    expect(fields.bodyMarkdown).toBe(true)
    expect(fields.logicalLineCount).toBe(4) // from `result`, NOT the empty content block
  })

  it('sizes a RemoteTrigger PRETTY JSON body (jsonBody) + a header', () => {
    const fields = hook(RESULT, toolResult({ tool_name: 'RemoteTrigger', status: 200, json: '{"ok":true,"name":"x","id":"y"}' }), undefined)!
    expect(fields.hasHeader).toBe(true)
    expect(fields.jsonBody).toBe(true)
    expect(fields.logicalLineCount!).toBeGreaterThan(1) // minified wire -> multi-line pretty
  })

  it('sizes a TaskOutput task.output (a different wire field than the content block) + a header', () => {
    const fields = hook(RESULT, toolResult({ tool_name: 'TaskOutput', task: { output: 'a\nb\nc' } }), undefined)!
    expect(fields.hasHeader).toBe(true)
    expect(fields.bodyMarkdown).toBe(false)
    expect(fields.logicalLineCount).toBe(3)
    expect(fields.textLength).toBe('a\nb\nc'.length)
    // Empty output -> generic (still a header via isAgentOrTaskResult; body = content block).
    const empty = hook(RESULT, toolResult({ tool_name: 'TaskOutput', task: {} }), undefined)!
    expect(empty.hasHeader).toBe(true)
    expect(empty.lineLengths).toBeUndefined() // body NOT overridden
  })

  it('sizes the \\r-normalized Bash output and widens the collapse threshold for progress runs', () => {
    const plain = hook(RESULT, toolResult({ tool_name: 'Bash', stdout: 'a\nb' }), undefined)!
    expect(plain.logicalLineCount).toBe(2)
    expect(plain.collapsedRowThreshold).toBeUndefined()
    // A single `\r`-overwrite physical line (8 segments) collapses to 7 displayed
    // lines, and the threshold widens to PROGRESS_MAX_ROWS so the gate matches.
    const progress = hook(RESULT, toolResult({ tool_name: 'Bash', stdout: 'p1\rp2\rp3\rp4\rp5\rp6\rp7\rp8' }), undefined)!
    expect(progress.logicalLineCount).toBe(7)
    expect(progress.collapsedRowThreshold).toBe(7)
  })

  it('sizes a Grep/Glob structured body (filenames / content blob) + a summary line', () => {
    const grep = hook(RESULT, toolResult({ tool_name: 'Grep', content: 'a.ts:1:foo\nb.ts:2:bar', numFiles: 2, numLines: 2 }), undefined)!
    expect(grep.summaryLineCount).toBe(1)
    expect(grep.logicalLineCount).toBe(2)
    // Glob renders from tool_use_result.filenames (content is ''), not the raw string.
    const glob = hook(RESULT, toolResult({ tool_name: 'Glob', filenames: ['a.ts', 'b.ts', 'c.ts', 'd.ts', 'e.ts'] }), undefined)!
    expect(glob.summaryLineCount).toBe(1)
    expect(glob.logicalLineCount).toBe(5) // 5 file rows, even though content is empty
  })

  it('charges an ExitPlanMode approval header + "Plan file:" prompt, zero body', () => {
    const withFile = hook(RESULT, toolResult({ tool_name: 'ExitPlanMode', filePath: 'plan.md' }), undefined)!
    expect(withFile.hasHeader).toBe(true)
    expect(withFile.summaryLineCount).toBe(1)
    expect(withFile.textLength).toBe(0)
    expect(withFile.lineLengths).toEqual([])
    // No plan file -> just the "Plan approved" header, no prompt.
    expect(hook(RESULT, toolResult({ tool_name: 'ExitPlanMode' }), undefined)!.summaryLineCount).toBe(0)
  })

  it('marks an MCP body uncollapsed + surfaces the args/structured pre-block line counts', () => {
    const sibling = parsed({
      parentObject: { type: 'assistant', message: { content: [{ type: 'tool_use', name: 'mcp__srv__tool', input: { query: 'hello', limit: 5 } }] } },
    })
    const fields = hook(RESULT, toolResult({ tool_name: 'mcp__srv__tool', structuredContent: { a: 1, b: 2 } }), sibling)!
    expect(fields.uncollapsed).toBe(true)
    expect(fields.bodyMarkdown).toBe(true)
    expect(fields.hasHeader).toBe(false)
    expect(fields.argsLineCount!).toBeGreaterThan(0)
    expect(fields.structuredLineCount!).toBeGreaterThan(0)
    // No args / structured -> still uncollapsed, but no extra-block counts.
    const bare = hook(RESULT, toolResult({ tool_name: 'mcp__srv__tool' }), undefined)!
    expect(bare.uncollapsed).toBe(true)
    expect(bare.argsLineCount).toBeUndefined()
    expect(bare.structuredLineCount).toBeUndefined()
  })

  it('an errored catch-all result falls back to the generic path (no structured sizing)', () => {
    // isError -> ToolSearch's guard yields the catch-all error path (header + error
    // text); the structured-view fields must NOT apply (the generic isError path
    // sizes the full body, no collapse).
    const fields = hook(RESULT, toolResult({ tool_name: 'ToolSearch', matches: ['a', 'b', 'c', 'd'] }, { isError: true }), undefined)!
    expect(fields.uncollapsed).toBeUndefined()
    expect(fields.lineLengths).toBeUndefined()
  })

  it('an errored Grep/Glob sizes the header-less mono fallback (no summary line)', () => {
    // SearchResultBody is dispatched even on is_error -> it draws a mono fallback
    // body with NO ToolStatusHeader and NO summary, so the estimate must size the
    // mono error content, not the structured summary+list success layout.
    const fields = hook(RESULT, toolResult({ tool_name: 'Grep', content: 'x', numFiles: 1 }, { isError: true }), undefined)!
    expect(fields.summaryLineCount).toBeUndefined()
    expect(fields.bodyMarkdown).toBe(false)
    expect(fields.lineLengths).toBeDefined() // the fallback error body IS sized
  })

  it('an errored ExitPlanMode (feedback) is sized as markdown, not mono', () => {
    // ExitPlanModeResultView renders the is_error feedback via MarkdownText (14px
    // markdown + block gaps), so the generic fallthrough must flag bodyMarkdown.
    expect(hook(RESULT, toolResult({ tool_name: 'ExitPlanMode' }, { isError: true }), undefined)!.bodyMarkdown).toBe(true)
  })
})

describe('claude heightMetrics result_divider detail', () => {
  it('counts the error detail lines from errors[]', () => {
    const p = parsed({ topLevel: { type: 'result', is_error: true, subtype: 'error_during_execution', errors: ['boom', 'bad'] } })
    const fields = hook(DIVIDER, p, undefined)!
    expect(fields.textLength).toBe('boom\nbad'.length)
    expect(fields.logicalLineCount).toBe(2)
    // Per-hard-line lengths MUST be fed so estimateSingleLineMeta sums each errors[]
    // entry's wrap (the pre-wrap <pre>); without them it under-counts several long errors.
    expect(fields.lineLengths).toEqual([4, 3]) // 'boom', 'bad'
  })

  it('returns null for a successful divider (no detail)', () => {
    const p = parsed({ topLevel: { type: 'result', is_error: false, subtype: 'success' } })
    expect(hook(DIVIDER, p, undefined)).toBeNull()
  })
})
