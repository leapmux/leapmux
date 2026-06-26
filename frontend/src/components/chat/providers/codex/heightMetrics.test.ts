import type { RowUiState } from '../../chatHeightEstimator'
import type { MessageCategory } from '../../messageClassification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { PROGRESS_MAX_ROWS } from '~/lib/normalizeProgressOutput'
import { CODEX_ITEM, CODEX_STATUS } from '~/types/toolMessages'
import { providerFor } from '../registry'
import './plugin'

const EMPTY: ParsedMessageContent = { rawText: '', topLevel: null, parentObject: undefined, wrapper: null }
// Codex ignores the row UI state; pass a collapsed default so the tests stay 3-arg.
const NO_UI_STATE: RowUiState = { collapsed: false, expanded: false, toolBodyExpanded: false, diffView: 'unified' }
const baseHook = providerFor(AgentProvider.CODEX)!.heightMetrics!
const hook = (c: MessageCategory, p: ParsedMessageContent, t: ParsedMessageContent | undefined) => baseHook(c, p, t, NO_UI_STATE)

/** A Codex tool_use category whose `toolUse` is the (already-unwrapped) item. */
function fileChange(item: Record<string, unknown>): MessageCategory {
  return { kind: 'tool_use', toolName: CODEX_ITEM.FILE_CHANGE, toolUse: { type: CODEX_ITEM.FILE_CHANGE, ...item }, content: [] }
}

/** A Codex tool_use category for an arbitrary (already-unwrapped) item + toolName. */
function codexToolUse(item: Record<string, unknown>, toolName: string): MessageCategory {
  return { kind: 'tool_use', toolName, toolUse: item, content: [] }
}

const REASONING: MessageCategory = { kind: 'assistant_thinking' }
/** A parsed message whose parentObject is an (unwrapped) Codex reasoning item. */
function reasoningParsed(item: Record<string, unknown>): ParsedMessageContent {
  return { rawText: '', topLevel: item, parentObject: item, wrapper: null }
}

describe('codex heightMetrics', () => {
  it('extracts diff rows from a completed fileChange unified diff', () => {
    const cat = fileChange({
      status: CODEX_STATUS.COMPLETED,
      changes: [{ path: 'a.txt', diff: '@@ -1,2 +1,2 @@\n line1\n-old\n+new\n line3' }],
    })
    const fields = hook(cat, EMPTY, undefined)!
    expect(fields).not.toBeNull()
    expect(fields.diffUnifiedRows).toBe(4) // context, -old, +new, context
    expect(fields.diffAdded).toBe(1)
    expect(fields.diffRemoved).toBe(1)
    // A single-file edit is one block with no per-file label row.
    expect(fields.diffBlockCount).toBe(1)
    expect(fields.diffPerFileLabelRows).toBeUndefined()
    // Completed fileChange mounts headerless (ToolResultMessage / bare toolMessage),
    // so estimateDiffRow must NOT charge a tool-title header for it.
    expect(fields.toolUseRendersResultBody).toBe(true)
  })

  it('sizes a multi-file fileChange as N blocks with N per-file labels', () => {
    // Two files, one hunk each -- the renderer stacks two diff blocks, each with
    // a per-file label row (path + stats badge). The estimate must charge chrome
    // PER block and count the labels, not collapse both files into one block.
    const cat = fileChange({
      status: CODEX_STATUS.COMPLETED,
      changes: [
        { path: 'a.txt', diff: '@@ -1,2 +1,2 @@\n line1\n-old\n+new\n line3' },
        { path: 'b.txt', diff: '@@ -10,1 +10,2 @@\n keep\n+added' },
      ],
    })
    const fields = hook(cat, EMPTY, undefined)!
    expect(fields.diffBlockCount).toBe(2)
    expect(fields.diffPerFileLabelRows).toBe(2)
    expect(fields.toolUseRendersResultBody).toBe(true) // headerless multi-file diff
    expect(fields.diffHunkCount).toBe(2) // one hunk per file
    expect(fields.diffAdded).toBe(2) // 1 + 1
    expect(fields.diffRemoved).toBe(1) // 1 + 0
    // Per-source separator counting: two single-hunk files have no between-hunk
    // gap. The old flattened path compared file-B's oldStart (10) against
    // file-A's hunk end and spuriously counted a cross-file separator.
    expect(fields.diffSeparatorRows).toBe(0)
  })

  it('keeps per-hunk boundaries for a multi-hunk diff (between-hunk separator)', () => {
    const diff = [
      '@@ -1,3 +1,3 @@',
      ' a',
      '-b',
      '+B',
      '@@ -10,3 +10,3 @@',
      ' x',
      '-y',
      '+Y',
    ].join('\n')
    const fields = hook(fileChange({ status: CODEX_STATUS.COMPLETED, changes: [{ diff }] }), EMPTY, undefined)!
    expect(fields.diffHunkCount).toBe(2)
    expect(fields.diffSeparatorRows).toBe(1)
    expect(fields.diffAdded).toBe(2)
    expect(fields.diffRemoved).toBe(2)
  })

  it('sizes a single simple-add change as an all-added diff', () => {
    const cat = fileChange({ status: CODEX_STATUS.COMPLETED, changes: [{ kind: 'add', diff: 'new file\nsecond line' }] })
    const fields = hook(cat, EMPTY, undefined)!
    expect(fields.diffAdded).toBe(2)
    expect(fields.diffRemoved).toBe(0)
    expect(fields.toolUseRendersResultBody).toBe(true) // headerless all-added diff
  })

  it('sizes a single simple-delete change as one header-LESS "Deleted <path>" markdown line', () => {
    // The renderer draws a bare "Deleted `path`" markdown line via ToolResultMessage
    // (displayKind="markdown", no status header) -- the estimate must be headerless,
    // not estimateToolUseHeader's phantom tool header + summary.
    const cat = fileChange({ status: CODEX_STATUS.COMPLETED, changes: [{ kind: 'delete', path: 'gone.ts' }] })
    const fields = hook(cat, EMPTY, undefined)!
    expect(fields.logicalLineCount).toBe(1)
    expect(fields.textLength).toBe('Deleted `gone.ts`'.length)
    expect(fields.toolUseRendersResultBody).toBe(true) // headerless result-style body
    expect(fields.toolHeaderLine).toBeUndefined() // no ToolUseLayout tool title
    expect(fields.bodyMarkdown).toBe(true) // rendered as markdown
  })

  it('sizes a completed fileChange with no parsable diffs as a header-less prompt row', () => {
    // Changes present but none parsed to a diff (and not a simple add/delete): the
    // renderer draws a header-LESS "N files changed" prompt, so the estimate must size
    // one prompt row, not fall through to a phantom tool-header estimate.
    const cat = fileChange({ status: CODEX_STATUS.COMPLETED, changes: [{ path: 'a.txt', diff: 'garbage with no hunks' }] })
    const fields = hook(cat, EMPTY, undefined)!
    expect(fields.toolUseRendersResultBody).toBe(true)
    expect(fields.summaryLineCount).toBe(1)
    expect(fields.toolHeaderLine).toBeUndefined() // header-less prompt, not a tool header
  })

  it('returns null for an in-progress fileChange (no diff renders yet)', () => {
    const cat = fileChange({ status: 'in_progress', changes: [{ path: 'a.txt', diff: '@@ -1,1 +1,1 @@\n-old\n+new' }] })
    expect(hook(cat, EMPTY, undefined)).toBeNull()
  })

  it('returns null for a webSearch tool_use (settled single-query form is header-only)', () => {
    const cat = codexToolUse({ type: CODEX_ITEM.WEB_SEARCH, status: CODEX_STATUS.COMPLETED }, 'webSearch')
    expect(hook(cat, EMPTY, undefined)).toBeNull()
  })

  describe('commandExecution (settled result body)', () => {
    it('sizes a completed command as a collapsible mono result body (no tool title)', () => {
      const cat = codexToolUse({ type: CODEX_ITEM.COMMAND_EXECUTION, status: CODEX_STATUS.COMPLETED, aggregatedOutput: 'a\nb\nc\nd\ne' }, 'commandExecution')
      const fields = hook(cat, EMPTY, undefined)!
      expect(fields.toolUseRendersResultBody).toBe(true)
      expect(fields.toolHeaderLine).toBeUndefined() // ToolResultMessage, not ToolUseLayout
      expect(fields.bodyMarkdown).toBe(false)
      expect(fields.logicalLineCount).toBe(5)
      expect(fields.collapsed).toBe(false) // NO_UI_STATE is expanded
      expect(fields.hasHeader).toBe(false) // success -> no status header
    })

    it('threads the resolved collapse state through to the body', () => {
      const cat = codexToolUse({ type: CODEX_ITEM.COMMAND_EXECUTION, status: CODEX_STATUS.COMPLETED, aggregatedOutput: 'x' }, 'commandExecution')
      // The `hook` helper pins NO_UI_STATE; call baseHook directly to vary the state.
      const collapsed = baseHook(cat, EMPTY, undefined, { ...NO_UI_STATE, collapsed: true })!
      expect(collapsed.collapsed).toBe(true)
    })

    it('marks a non-zero exit with an error status header', () => {
      const cat = codexToolUse({ type: CODEX_ITEM.COMMAND_EXECUTION, status: CODEX_STATUS.FAILED, exitCode: 1, aggregatedOutput: 'boom' }, 'commandExecution')
      const fields = hook(cat, EMPTY, undefined)!
      expect(fields.hasHeader).toBe(true)
      expect(fields.isError).toBe(true)
    })

    it('returns null while in progress (streams/measures at the tail)', () => {
      const cat = codexToolUse({ type: CODEX_ITEM.COMMAND_EXECUTION, status: CODEX_STATUS.IN_PROGRESS, aggregatedOutput: 'partial' }, 'commandExecution')
      expect(hook(cat, EMPTY, undefined)).toBeNull()
    })

    it('widens the collapse threshold for carriage-return progress output', () => {
      const cat = codexToolUse({ type: CODEX_ITEM.COMMAND_EXECUTION, status: CODEX_STATUS.COMPLETED, aggregatedOutput: 'p 1\rp 2\rp 3\rp 4\rp 5\rp 6\rp 7\rp 8' }, 'commandExecution')
      const fields = hook(cat, EMPTY, undefined)!
      expect(fields.collapsedRowThreshold).toBe(PROGRESS_MAX_ROWS)
    })
  })

  describe('mcpToolCall (alwaysVisible full body)', () => {
    it('sizes a settled MCP call as a full markdown body with args + tool header', () => {
      const cat = codexToolUse({
        type: CODEX_ITEM.MCP_TOOL_CALL,
        status: CODEX_STATUS.COMPLETED,
        arguments: { path: 'a.ts', limit: 50 },
        result: { content: [{ type: 'text', text: 'result line one\nresult line two' }] },
      }, 'someTool')
      const fields = hook(cat, EMPTY, undefined)!
      expect(fields.toolUseRendersResultBody).toBe(true)
      expect(fields.toolHeaderLine).toBe(true)
      expect(fields.uncollapsed).toBe(true) // alwaysVisible when terminal
      expect(fields.bodyMarkdown).toBe(true)
      expect(fields.logicalLineCount).toBe(2)
      expect(fields.argsLineCount).toBeGreaterThan(0)
    })

    it('returns null while in progress (collapsed/streaming)', () => {
      const cat = codexToolUse({ type: CODEX_ITEM.MCP_TOOL_CALL, status: CODEX_STATUS.IN_PROGRESS, arguments: {} }, 'someTool')
      expect(hook(cat, EMPTY, undefined)).toBeNull()
    })

    it('counts image content blocks in the MCP result body', () => {
      const cat = codexToolUse({
        type: CODEX_ITEM.MCP_TOOL_CALL,
        status: CODEX_STATUS.COMPLETED,
        arguments: {},
        result: { content: [{ type: 'text', text: 'see image' }, { type: 'image', data: 'b64', mimeType: 'image/png' }] },
      }, 'someTool')
      const fields = hook(cat, EMPTY, undefined)!
      expect(fields.imageCount).toBe(1)
    })
  })

  describe('collabAgentToolCall (collapsible prompt)', () => {
    it('sizes a spawnAgent prompt as a collapsible markdown body with a tool header', () => {
      const cat = codexToolUse({ type: CODEX_ITEM.COLLAB_AGENT_TOOL_CALL, tool: 'spawnAgent', prompt: 'do the thing\nwith detail' }, 'collabAgentToolCall')
      const fields = hook(cat, EMPTY, undefined)!
      expect(fields.toolUseRendersResultBody).toBe(true)
      expect(fields.toolHeaderLine).toBe(true)
      expect(fields.bodyMarkdown).toBe(true)
      expect(fields.logicalLineCount).toBe(2)
    })

    it('returns null for a collab call with no prompt (wait/closeAgent -> header only)', () => {
      const cat = codexToolUse({ type: CODEX_ITEM.COLLAB_AGENT_TOOL_CALL, tool: 'closeAgent' }, 'collabAgentToolCall')
      expect(hook(cat, EMPTY, undefined)).toBeNull()
    })
  })

  it('returns null for a non-tool_use category', () => {
    expect(hook({ kind: 'tool_result' }, EMPTY, undefined)).toBeNull()
  })

  it('does not throw on a malformed non-array changes payload', () => {
    // A truthy non-array `changes` (a malformed wire payload) must not crash the
    // height-estimate memo, which has no try/catch -- buildFileChangeShape treats
    // it as empty rather than iterating it. The old `|| []` let a truthy object
    // through and threw on the `for...of`.
    const cat = fileChange({ status: CODEX_STATUS.COMPLETED, changes: {} as unknown as [] })
    expect(() => hook(cat, EMPTY, undefined)).not.toThrow()
    expect(hook(cat, EMPTY, undefined)).toBeNull()
  })

  // Codex reasoning persists its text in item.summary/content (not message.content),
  // so extractText sizes an empty bubble; the hook must size the persisted text or an
  // expanded reasoning row under-estimates off-screen.
  it('sizes a Codex reasoning bubble from its persisted summary text', () => {
    const item = { type: CODEX_ITEM.REASONING, summary: ['first thought', 'second thought\nwith a wrapped line'] }
    const fields = hook(REASONING, reasoningParsed(item), undefined)!
    expect(fields).not.toBeNull()
    const text = 'first thought\nsecond thought\nwith a wrapped line'
    expect(fields.textLength).toBe(text.length)
    expect(fields.logicalLineCount).toBe(3)
  })

  it('falls back to reasoning content when there is no summary', () => {
    const fields = hook(REASONING, reasoningParsed({ type: CODEX_ITEM.REASONING, summary: [], content: ['c1', 'c2'] }), undefined)!
    expect(fields.textLength).toBe('c1\nc2'.length)
    expect(fields.logicalLineCount).toBe(2)
  })

  it('returns null for an empty reasoning item (renders nothing)', () => {
    expect(hook(REASONING, reasoningParsed({ type: CODEX_ITEM.REASONING, summary: [], content: [] }), undefined)).toBeNull()
  })

  it('returns null for an assistant_thinking that is not a Codex reasoning item', () => {
    expect(hook(REASONING, EMPTY, undefined)).toBeNull()
  })

  it('does not throw on an array whose elements are non-objects (null / primitive)', () => {
    // Array.isArray passes for `[null, 5]`, but reading `change.diff` / `change.kind`
    // off a non-object element throws inside the no-try/catch estimate memo. The
    // per-element isObject filter (mirroring the acp/pi extractors) drops them, and
    // a valid change alongside still extracts.
    const cat = fileChange({
      status: CODEX_STATUS.COMPLETED,
      changes: [null, 5, { path: 'a.txt', diff: '@@ -1,1 +1,1 @@\n-old\n+new' }] as unknown as [],
    })
    expect(() => hook(cat, EMPTY, undefined)).not.toThrow()
    const fields = hook(cat, EMPTY, undefined)!
    expect(fields.diffAdded).toBe(1)
    expect(fields.diffRemoved).toBe(1)
  })
})
