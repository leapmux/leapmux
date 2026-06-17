import type { MessageCategory } from './messageClassification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { CODEX_ITEM, CODEX_STATUS } from '~/types/toolMessages'
import { buildHeightInput } from './chatHeightInput'
// Register the provider plugins so the heightMetrics dispatch resolves a plugin.
import './providers'

const STATE = { collapsed: false, expanded: true, toolBodyExpanded: false, diffView: 'unified' as const }

function parsed(over: Partial<ParsedMessageContent>): ParsedMessageContent {
  return { rawText: '', topLevel: null, parentObject: undefined, wrapper: null, ...over }
}

/** A Claude tool_result parsed message with a tool_use_result payload. */
function claudeResult(tur: Record<string, unknown>, isError = false): ParsedMessageContent {
  return parsed({
    parentObject: {
      message: { content: [{ type: 'tool_result', is_error: isError, content: 'x' }] },
      tool_use_result: tur,
    },
  })
}

describe('chatheightinput orchestration', () => {
  it('gives a provider diff precedence and applies diffView from UI state, skipping generic body fields', () => {
    const input = buildHeightInput({
      kind: 'tool_result',
      hasSpanLines: false,
      category: { kind: 'tool_result' },
      parsed: claudeResult({ filePath: 'a.ts', structuredPatch: [{ oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, lines: ['-old', '+new'] }] }),
      state: { ...STATE, diffView: 'split' },
      agentProvider: AgentProvider.CLAUDE_CODE,
    })
    expect((input.diffUnifiedRows ?? 0)).toBeGreaterThan(0)
    expect(input.diffView).toBe('split')
    // Diff precedence: the generic tool_result body fields are NOT computed.
    expect(input.collapsed).toBeUndefined()
    expect(input.isError).toBeUndefined()
  })

  it('only dispatches the hook when a registered provider is supplied', () => {
    const category: MessageCategory = {
      kind: 'tool_use',
      toolName: CODEX_ITEM.FILE_CHANGE,
      toolUse: { type: CODEX_ITEM.FILE_CHANGE, status: CODEX_STATUS.COMPLETED, changes: [{ path: 'a.txt', diff: '@@ -1,1 +1,1 @@\n-old\n+new' }] },
      content: [],
    }
    const base = { kind: 'tool_use:fileChange', toolName: 'fileChange', hasSpanLines: false, category, parsed: parsed({}), state: STATE }

    expect(buildHeightInput({ ...base, agentProvider: AgentProvider.CODEX }).diffUnifiedRows).toBeGreaterThan(0)
    // No provider -> no hook -> sized as a generic header-only tool_use (no diff).
    expect(buildHeightInput(base).diffUnifiedRows).toBeUndefined()
  })

  it('treats a failed (non-diff) result as headered via the generic is_error', () => {
    const input = buildHeightInput({
      kind: 'tool_result',
      hasSpanLines: false,
      category: { kind: 'tool_result' },
      parsed: claudeResult({ tool_name: 'Bash' }, true),
      state: STATE,
      agentProvider: AgentProvider.CLAUDE_CODE,
    })
    expect(input.isError).toBe(true)
    expect(input.hasHeader).toBe(true)
    expect(input.bodyMarkdown).toBe(false)
  })

  it('merges the hook bodyMarkdown + hasHeader onto the generic tool_result body', () => {
    const input = buildHeightInput({
      kind: 'tool_result',
      hasSpanLines: false,
      category: { kind: 'tool_result' },
      parsed: claudeResult({ agentId: 'abc', content: 'sub-agent result body' }),
      state: STATE,
      agentProvider: AgentProvider.CLAUDE_CODE,
    })
    expect(input.bodyMarkdown).toBe(true)
    expect(input.hasHeader).toBe(true)
    // Generic body text is still extracted (not a diff row).
    expect((input.textLength ?? 0)).toBeGreaterThan(0)
  })

  it('passes the paired tool_use sibling through to the hook (Write-create input fallback)', () => {
    const sibling = parsed({
      parentObject: { type: 'assistant', message: { content: [{ type: 'tool_use', name: 'Write', input: { file_path: 'n.ts', content: 'a\nb\nc' } }] } },
    })
    const input = buildHeightInput({
      kind: 'tool_result',
      hasSpanLines: false,
      category: { kind: 'tool_result' },
      parsed: claudeResult({ filePath: 'n.ts' }), // result carries no diff itself
      state: STATE,
      agentProvider: AgentProvider.CLAUDE_CODE,
      toolUseParsed: sibling,
    })
    expect(input.diffAdded).toBe(3)
  })

  it('keeps generic extraction when the hook contributes nothing (prose)', () => {
    const input = buildHeightInput({
      kind: 'assistant_text',
      hasSpanLines: false,
      category: { kind: 'assistant_text' },
      parsed: parsed({ parentObject: { message: { content: [{ type: 'text', text: 'hello\nworld' }] } } }),
      state: STATE,
      agentProvider: AgentProvider.CLAUDE_CODE,
    })
    expect(input.textLength).toBe('hello\nworld'.length)
    expect(input.logicalLineCount).toBe(2)
    expect(input.diffUnifiedRows).toBeUndefined()
  })

  it('sizes a whitespace-only Bash command from the whole command (matching deriveToolSummary)', () => {
    // firstNonEmptyLine of an all-whitespace command is null; deriveToolSummary falls
    // back to the whole command, so the estimate must too -- else it sizes a bare header
    // (textLength unset) where a body actually mounts.
    const category: MessageCategory = {
      kind: 'tool_use',
      toolName: 'Bash',
      toolUse: { type: 'tool_use', name: 'Bash', input: { command: '   \n   ' } },
      content: [],
    }
    const input = buildHeightInput({
      kind: 'tool_use:Bash',
      toolName: 'Bash',
      hasSpanLines: false,
      category,
      parsed: parsed({}),
      state: STATE, // toolBodyExpanded false -> the one-line summary path
      agentProvider: AgentProvider.CLAUDE_CODE,
    })
    expect(input.textLength).toBe('   \n   '.length)
    expect(input.logicalLineCount).toBe(1)
  })

  it('feeds per-line lengths for an EXPANDED multi-line Bash command (pre-wrap summary)', () => {
    // toolBodyExpanded renders the full multi-line command in a pre-wrap block, so each
    // hard line wraps independently. buildHeightInput must emit lineLengths -- without
    // them estimateToolUseHeader falls back to the flat total-wrap model and under-counts.
    const command = 'echo a\necho bb'
    const category: MessageCategory = {
      kind: 'tool_use',
      toolName: 'Bash',
      toolUse: { type: 'tool_use', name: 'Bash', input: { command } },
      content: [],
    }
    const input = buildHeightInput({
      kind: 'tool_use:Bash',
      toolName: 'Bash',
      hasSpanLines: false,
      category,
      parsed: parsed({}),
      state: { ...STATE, toolBodyExpanded: true }, // expanded -> full command body path
      agentProvider: AgentProvider.CLAUDE_CODE,
    })
    expect(input.textLength).toBe(command.length)
    expect(input.logicalLineCount).toBe(2)
    expect(input.lineLengths).toEqual([6, 7]) // 'echo a', 'echo bb'
  })

  it('extracts a generic TodoWrite count independent of the provider hook', () => {
    const category: MessageCategory = {
      kind: 'tool_use',
      toolName: 'TodoWrite',
      toolUse: { type: 'tool_use', name: 'TodoWrite', input: { todos: [{}, {}, {}] } },
      content: [],
    }
    const input = buildHeightInput({
      kind: 'tool_use:TodoWrite',
      toolName: 'TodoWrite',
      hasSpanLines: false,
      category,
      parsed: parsed({}),
      state: STATE,
      agentProvider: AgentProvider.CLAUDE_CODE,
    })
    expect(input.todoCount).toBe(3)
  })

  it('counts tool_result images from topLevel when parentObject is absent (matching extractText)', () => {
    // A tool_result whose content blocks live on topLevel (parentObject undefined):
    // extractText already falls back to topLevel, so countImages must too -- else the
    // text is sized but the images are dropped, under-estimating the row.
    const input = buildHeightInput({
      kind: 'tool_result',
      hasSpanLines: false,
      category: { kind: 'tool_result' },
      parsed: parsed({
        topLevel: {
          message: {
            content: [
              { type: 'tool_result', content: [{ type: 'image' }, { type: 'image' }] },
            ],
          },
        },
        parentObject: undefined,
      }),
      state: STATE,
      // No provider hook -> the generic tool_result path (with countImages) runs.
    })
    expect(input.imageCount).toBe(2)
  })
})
