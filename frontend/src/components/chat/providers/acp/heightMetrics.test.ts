import type { RowUiState } from '../../chatHeightEstimator'
import type { MessageCategory } from '../../messageClassification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { ACP_SESSION_UPDATE, ACP_TOOL_KIND } from '~/types/toolMessages'
import { providerFor } from '../registry'
// Register an ACP-based provider (opencode) so the shared ACP heightMetrics runs.
import '../opencode'

const EMPTY: ParsedMessageContent = { rawText: '', topLevel: null, parentObject: undefined, wrapper: null }
const COLLAPSED: RowUiState = { collapsed: false, expanded: false, toolBodyExpanded: false, diffView: 'unified' }
const EXPANDED: RowUiState = { ...COLLAPSED, toolBodyExpanded: true }
const baseHook = providerFor(AgentProvider.OPENCODE)!.heightMetrics!
const hook = (c: MessageCategory, p: ParsedMessageContent, t: ParsedMessageContent | undefined, state: RowUiState = COLLAPSED) => baseHook(c, p, t, state)

function toolUse(over: Record<string, unknown>): MessageCategory {
  return { kind: 'tool_use', toolName: ACP_TOOL_KIND.EDIT, toolUse: { kind: ACP_TOOL_KIND.EDIT, ...over }, content: [] }
}

function executeToolUse(over: Record<string, unknown>): MessageCategory {
  return {
    kind: 'tool_use',
    toolName: ACP_TOOL_KIND.EXECUTE,
    toolUse: { kind: ACP_TOOL_KIND.EXECUTE, sessionUpdate: ACP_SESSION_UPDATE.TOOL_CALL_UPDATE, status: 'completed', ...over },
    content: [],
  }
}

describe('acp heightMetrics', () => {
  it('sizes a tool_call_update diff from a content[] diff block (regression: was unsized)', () => {
    const cat = toolUse({
      sessionUpdate: ACP_SESSION_UPDATE.TOOL_CALL_UPDATE,
      status: 'completed',
      content: [{ type: 'diff', path: 'a.ts', oldText: 'a\nb\nc', newText: 'a\nB\nc' }],
    })
    const fields = hook(cat, EMPTY, undefined)!
    expect(fields).not.toBeNull()
    expect((fields.diffAdded ?? 0) + (fields.diffRemoved ?? 0)).toBeGreaterThan(0)
  })

  it('falls back to rawInput edit text when no content diff is present', () => {
    const cat = toolUse({
      sessionUpdate: ACP_SESSION_UPDATE.TOOL_CALL_UPDATE,
      status: 'completed',
      rawInput: { filePath: 'a.ts', oldText: 'x\ny', newText: 'x\nY' },
    })
    const fields = hook(cat, EMPTY, undefined)!
    expect((fields.diffAdded ?? 0) + (fields.diffRemoved ?? 0)).toBeGreaterThan(0)
  })

  it('returns null for a FAILED update (renders error text, not a diff)', () => {
    const cat = toolUse({
      sessionUpdate: ACP_SESSION_UPDATE.TOOL_CALL_UPDATE,
      status: 'failed',
      content: [{ type: 'diff', path: 'a.ts', oldText: 'a', newText: 'b' }],
    })
    expect(hook(cat, EMPTY, undefined)).toBeNull()
  })

  it('returns null for the initial (pending) tool_call -- header-only', () => {
    const cat = toolUse({
      sessionUpdate: ACP_SESSION_UPDATE.TOOL_CALL,
      content: [{ type: 'diff', path: 'a.ts', oldText: 'a', newText: 'b' }],
    })
    expect(hook(cat, EMPTY, undefined)).toBeNull()
  })

  it('sizes a settled execute as a tool header + command summary + collapsible output body', () => {
    const cat = executeToolUse({
      rawInput: { command: 'echo hi' },
      content: [{ type: 'content', content: { type: 'text', text: 'line1\nline2\nline3\nline4' } }],
    })
    const fields = hook(cat, EMPTY, undefined, COLLAPSED)!
    expect(fields.toolUseRendersResultBody).toBe(true)
    expect(fields.toolHeaderLine).toBe(true)
    expect(fields.summaryLineCount).toBe(1) // one-line command summary (collapsed default)
    expect(fields.logicalLineCount).toBe(4) // the output body
    expect(fields.collapsed).toBe(false) // COLLAPSED state is `collapsed: false` (expanded body)
  })

  it('counts the FULL multi-line command as summary rows when the command toggle is expanded', () => {
    const cat = executeToolUse({ rawInput: { command: 'echo one\necho two\necho three' } })
    const fields = hook(cat, EMPTY, undefined, EXPANDED)!
    // The expanded multi-line command shows all its lines above the (empty) output.
    expect(fields.summaryLineCount).toBe(3)
  })

  it('shows a single command summary row when the command is single-line', () => {
    const cat = executeToolUse({ rawInput: { command: 'echo hello' } })
    const fields = hook(cat, EMPTY, undefined, EXPANDED)!
    expect(fields.summaryLineCount).toBe(1)
  })

  it('marks a failed execute with a status header (renders error body, not a diff)', () => {
    const cat = executeToolUse({
      status: 'failed',
      rawInput: { command: 'boom' },
      content: [{ type: 'content', content: { type: 'text', text: 'command failed' } }],
    })
    const fields = hook(cat, EMPTY, undefined, COLLAPSED)!
    expect(fields.toolUseRendersResultBody).toBe(true)
    expect(fields.hasHeader).toBe(true)
    expect(fields.isError).toBe(true)
  })

  it('sizes a read body as a collapsible cat-n code body with a tool header', () => {
    const cat: MessageCategory = {
      kind: 'tool_use',
      toolName: ACP_TOOL_KIND.READ,
      toolUse: {
        kind: ACP_TOOL_KIND.READ,
        sessionUpdate: ACP_SESSION_UPDATE.TOOL_CALL_UPDATE,
        status: 'completed',
        rawInput: { path: 'a.ts' },
        content: [{ type: 'content', content: { type: 'text', text: '   1\tconst a = 1\n   2\tconst b = 2\n   3\tconst c = 3' } }],
      },
      content: [],
    }
    const fields = hook(cat, EMPTY, undefined, COLLAPSED)!
    expect(fields.toolUseRendersResultBody).toBe(true)
    expect(fields.toolHeaderLine).toBe(true)
    expect(fields.logicalLineCount).toBeGreaterThan(1)
  })

  it('sizes a read body from the PARSED cat-n lines, excluding a trailing reminder block', () => {
    // The raw text is a 3-line cat-n body PLUS a 3-line <system-reminder> block that
    // parseReadContent strips into a trailing alert (rendered only when expanded). The
    // estimate must count the PARSED body (3 lines), not the raw text (6) -- the renderer
    // collapses on items().length, so charging the reminder lines would trip the collapse
    // gate at the wrong row count and under-estimate the row when it renders in full.
    const cat: MessageCategory = {
      kind: 'tool_use',
      toolName: ACP_TOOL_KIND.READ,
      toolUse: {
        kind: ACP_TOOL_KIND.READ,
        sessionUpdate: ACP_SESSION_UPDATE.TOOL_CALL_UPDATE,
        status: 'completed',
        rawInput: { path: 'a.ts' },
        content: [{ type: 'content', content: { type: 'text', text: '   1\tconst a = 1\n   2\tconst b = 2\n   3\tconst c = 3\n<system-reminder>\nAvoid excessive reads.\n</system-reminder>' } }],
      },
      content: [],
    }
    const fields = hook(cat, EMPTY, undefined, COLLAPSED)!
    expect(fields.logicalLineCount).toBe(3) // the 3 cat-n body lines, NOT the 6 raw lines
  })

  it('sizes a non-cat-n read as a collapsible mono body (renders CollapsibleContent, not a bare header)', () => {
    // filePath resolves but the output is NOT cat-n (no leading line numbers), so
    // source.lines === null. The renderer's body().kind === 'none' branch still mounts a
    // collapsible ansi-or-pre <pre>; the estimate must size that body, not return null.
    const cat: MessageCategory = {
      kind: 'tool_use',
      toolName: ACP_TOOL_KIND.READ,
      toolUse: {
        kind: ACP_TOOL_KIND.READ,
        sessionUpdate: ACP_SESSION_UPDATE.TOOL_CALL_UPDATE,
        status: 'completed',
        rawInput: { path: 'a.bin' },
        content: [{ type: 'content', content: { type: 'text', text: 'raw line one\nraw line two\nraw line three\nraw line four' } }],
      },
      content: [],
    }
    const fields = hook(cat, EMPTY, undefined, COLLAPSED)!
    expect(fields).not.toBeNull()
    expect(fields.toolUseRendersResultBody).toBe(true)
    expect(fields.toolHeaderLine).toBe(true)
    expect(fields.logicalLineCount).toBe(4)
  })

  it('returns null for a read whose output is empty (a true header-only row)', () => {
    const cat: MessageCategory = {
      kind: 'tool_use',
      toolName: ACP_TOOL_KIND.READ,
      toolUse: {
        kind: ACP_TOOL_KIND.READ,
        sessionUpdate: ACP_SESSION_UPDATE.TOOL_CALL_UPDATE,
        status: 'completed',
        rawInput: { path: 'a.bin' },
        content: [{ type: 'content', content: { type: 'text', text: '' } }],
      },
      content: [],
    }
    expect(hook(cat, EMPTY, undefined, COLLAPSED)).toBeNull()
  })

  it('sizes a search result as a single "Found N matches" summary line', () => {
    const cat: MessageCategory = {
      kind: 'tool_use',
      toolName: ACP_TOOL_KIND.SEARCH,
      toolUse: {
        kind: ACP_TOOL_KIND.SEARCH,
        sessionUpdate: ACP_SESSION_UPDATE.TOOL_CALL_UPDATE,
        status: 'completed',
        rawInput: { pattern: 'foo' },
        rawOutput: { metadata: { matches: 7 } },
      },
      content: [],
    }
    const fields = hook(cat, EMPTY, undefined, COLLAPSED)!
    expect(fields.toolUseRendersResultBody).toBe(true)
    expect(fields.toolHeaderLine).toBe(true)
    expect(fields.summaryLineCount).toBe(1)
  })
})
