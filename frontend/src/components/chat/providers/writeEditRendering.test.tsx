import type { MessageCategory } from '../messageClassifier'
import type { MessageContentRenderContext } from '../messageContentRenderer'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import { render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider, ContentCompression } from '~/generated/proto/leapmux/v1/agent_pb'
import { parseMessageContent } from '~/lib/messageParser'
import { testMessageSources } from '~/test-support/messageRenderSources'
import { resolveMessageForRendering } from './registry'
import './claude/plugin'
import './codex/plugin'
import './opencode/plugin'
import './pi/plugin'
import './testMocks'

vi.mock('~/lib/shikiWorkerClient', () => ({
  tokenizeAsync: async (_lang: string, code: string) => code.split('\n').map(() => []),
}))

vi.mock('~/lib/tokenCache', () => ({
  getCachedTokens: () => null,
  makeKey: (lang: string, code: string) => `${lang}\0${code}`,
}))

const { renderMessageContent } = await import('../messageContentRenderer')

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

function makeFakeMessage(content: Record<string, unknown>): AgentChatMessage {
  return {
    content: new TextEncoder().encode(JSON.stringify(content)),
    contentCompression: ContentCompression.NONE,
  } as unknown as AgentChatMessage
}

interface ToolUsePayload {
  type: string
  message: { role: string, content: Array<Record<string, unknown>> }
}

function makeClaudeToolUseMessage(name: string, input: Record<string, unknown>): ToolUsePayload {
  return {
    type: 'assistant',
    message: {
      role: 'assistant',
      content: [{ type: 'tool_use', id: 'toolu_x', name, input }],
    },
  }
}

function makeClaudeToolResultMessage(
  toolUseResult: Record<string, unknown> | undefined,
  content: string = 'OK',
  options: { isError?: boolean, toolUseResultRaw?: unknown } = {},
) {
  const block: Record<string, unknown> = { type: 'tool_result', tool_use_id: 'toolu_x', content }
  if (options.isError !== undefined)
    block.is_error = options.isError
  const envelope: Record<string, unknown> = {
    type: 'user',
    message: { role: 'user', content: [block] },
  }
  if (options.toolUseResultRaw !== undefined)
    envelope.tool_use_result = options.toolUseResultRaw
  else if (toolUseResult)
    envelope.tool_use_result = toolUseResult
  return envelope
}

function renderClaudeToolUse(name: string, input: Record<string, unknown>, context?: MessageContentRenderContext) {
  const parsed = makeClaudeToolUseMessage(name, input)
  const category = { kind: 'tool_use' } as MessageCategory
  return render(() => renderMessageContent(parsed, context, category, AgentProvider.CLAUDE_CODE))
}

function renderClaudeToolResult(parsed: Record<string, unknown>, context?: MessageContentRenderContext) {
  const category: MessageCategory = { kind: 'tool_result' }
  return render(() => renderMessageContent(parsed, context, category, AgentProvider.CLAUDE_CODE))
}

function makePiToolStart(toolName: string, args: Record<string, unknown>) {
  return {
    type: 'tool_execution_start',
    toolCallId: `call_${toolName}_1`,
    toolName,
    args,
  }
}

function makePiToolEnd(toolName: string, result: Record<string, unknown>, isError = false) {
  return {
    type: 'tool_execution_end',
    toolCallId: `call_${toolName}_1`,
    toolName,
    result,
    isError,
  }
}

function renderPiToolUse(toolName: string, args: Record<string, unknown>, context?: MessageContentRenderContext) {
  const toolUse = makePiToolStart(toolName, args)
  const category: MessageCategory = { kind: 'tool_use' }
  return render(() => renderMessageContent(toolUse, context, category, AgentProvider.PI))
}

function renderPiToolResult(toolName: string, resultPayload: Record<string, unknown>, startArgs: Record<string, unknown>, isError = false) {
  const start = makePiToolStart(toolName, startArgs)
  const end = makePiToolEnd(toolName, resultPayload, isError)
  const category: MessageCategory = { kind: 'tool_result' }
  return render(() => renderMessageContent(end, { spanType: toolName, sources: testMessageSources({ request: () => resolveMessageForRendering(parseMessageContent(makeFakeMessage(start)), AgentProvider.PI) }) }, category, AgentProvider.PI))
}

// ---------------------------------------------------------------------------
// Claude Code: a tool_use states the change it REQUESTS
// ---------------------------------------------------------------------------

// The pending row draws the shared "Requested changes" card every provider uses
// for a write that has not landed. It claims nothing about the file, and the
// result row replaces it with the diff that did land.
describe('claude Edit/Write tool_use states its requested change', () => {
  it('edit tool_use shows file path, stats and the substitution it asks for', () => {
    const { container } = renderClaudeToolUse('Edit', {
      file_path: '/tmp/example.ts',
      old_string: 'oldLineMarkerABC',
      new_string: 'newLineMarkerABC',
    })
    const text = container.textContent ?? ''
    expect(text).toContain('example.ts')
    expect(text).toContain('Requested changes')
    expect(text).toContain('oldLineMarkerABC')
    expect(text).toContain('newLineMarkerABC')
  })

  it('write tool_use shows file path, line count and the body it asks for', () => {
    const { container } = renderClaudeToolUse('Write', {
      file_path: '/tmp/new.ts',
      content: 'package main\n\nfunc main() {}\n',
    })
    const text = container.textContent ?? ''
    expect(text).toContain('new.ts')
    expect(text).toContain('Requested changes')
    expect(text).toContain('package main')
  })
})

// ---------------------------------------------------------------------------
// Claude Code: tool_result diff selection
// ---------------------------------------------------------------------------

describe('claude Edit tool_result diff selection', () => {
  const editToolUseParsed = resolveMessageForRendering(parseMessageContent(makeFakeMessage(
    makeClaudeToolUseMessage('Edit', {
      file_path: '/tmp/file.ts',
      old_string: 'fallbackOldZZZ',
      new_string: 'fallbackNewZZZ',
    }) as unknown as Record<string, unknown>,
  )), AgentProvider.CLAUDE_CODE)

  it('renders the result-side structuredPatch when present (and ignores tool_use fallback)', () => {
    const parsed = makeClaudeToolResultMessage({
      tool_name: 'Edit',
      filePath: '/tmp/file.ts',
      structuredPatch: [
        { oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, lines: ['-resultPatchOldQ', '+resultPatchNewQ'] },
      ],
      oldString: 'somethingElse',
      newString: 'somethingElse',
    }, 'File modified.')
    const { container } = renderClaudeToolResult(parsed, { sources: testMessageSources({ request: () => (editToolUseParsed) }) })
    const text = container.textContent ?? ''
    expect(text).toContain('resultPatchOldQ')
    expect(text).toContain('resultPatchNewQ')
    expect(text).not.toContain('fallbackOldZZZ')
    expect(text).not.toContain('fallbackNewZZZ')
    // Success-text is hidden when the diff is going to render.
    expect(text).not.toContain('File modified.')
  })

  it('falls back to the linked tool_use diff when result has no structured patch', () => {
    const parsed = makeClaudeToolResultMessage({
      tool_name: 'Edit',
      filePath: '/tmp/file.ts',
    }, 'Modified.')
    const { container } = renderClaudeToolResult(parsed, { sources: testMessageSources({ request: () => (editToolUseParsed) }) })
    const text = container.textContent ?? ''
    expect(text).toContain('fallbackOldZZZ')
    expect(text).toContain('fallbackNewZZZ')
  })

  it('renders error text instead of the linked tool_use diff when is_error is true', () => {
    const parsed = makeClaudeToolResultMessage({
      tool_name: 'Edit',
      filePath: '/tmp/file.ts',
    }, 'Found 2 occurrences; old_string must be unique.', { isError: true })
    const { container } = renderClaudeToolResult(parsed, { sources: testMessageSources({ request: () => (editToolUseParsed) }) })
    const text = container.textContent ?? ''
    expect(text).toContain('Found 2 occurrences')
    expect(text).not.toContain('fallbackOldZZZ')
    expect(text).not.toContain('fallbackNewZZZ')
  })

  it('renders the result content text when neither result nor tool_use carries a diff', () => {
    const noDiffToolUse = resolveMessageForRendering(parseMessageContent(makeFakeMessage(
      makeClaudeToolUseMessage('Edit', { file_path: '/tmp/x.ts' }) as unknown as Record<string, unknown>,
    )), AgentProvider.CLAUDE_CODE)
    const parsed = makeClaudeToolResultMessage({ tool_name: 'Edit' }, 'Plain text result.')
    const { container } = renderClaudeToolResult(parsed, { sources: testMessageSources({ request: () => (noDiffToolUse) }) })
    const text = container.textContent ?? ''
    expect(text).toContain('Plain text result.')
  })

  it('renders the result-side structuredPatch when there is no linked tool_use at all', () => {
    const parsed = makeClaudeToolResultMessage({
      tool_name: 'Edit',
      filePath: '/tmp/file.ts',
      structuredPatch: [
        { oldStart: 2, oldLines: 1, newStart: 2, newLines: 1, lines: ['-standaloneOld', '+standaloneNew'] },
      ],
    }, 'Modified.')
    const { container } = renderClaudeToolResult(parsed, { spanType: 'Edit' })
    const text = container.textContent ?? ''
    expect(text).toContain('standaloneOld')
    expect(text).toContain('standaloneNew')
  })

  // Regression: when Claude rejects an Edit (e.g. "File has not been read yet"),
  // the wire-shape carries `is_error: true` and a `<tool_use_error>` wrapper in
  // content. Previously we still rendered the tool_use input as a diff, which
  // misled users into thinking the edit had been applied.
  it('renders the error message and suppresses the fallback diff when is_error=true', () => {
    const errorContent = '<tool_use_error>File has not been read yet. Read it first before writing to it.</tool_use_error>'
    const parsed = makeClaudeToolResultMessage(undefined, errorContent, {
      isError: true,
      toolUseResultRaw: 'Error: File has not been read yet. Read it first before writing to it.',
    })
    const { container } = renderClaudeToolResult(parsed, { spanType: 'Edit', sources: testMessageSources({ request: () => (editToolUseParsed) }) })
    const text = container.textContent ?? ''
    expect(text).toContain('File has not been read yet')
    expect(text).not.toContain('fallbackOldZZZ')
    expect(text).not.toContain('fallbackNewZZZ')
    expect(text).toContain('Error')
  })
})

describe('claude Write tool_result diff selection', () => {
  const writeToolUseParsed = resolveMessageForRendering(parseMessageContent(makeFakeMessage(
    makeClaudeToolUseMessage('Write', {
      file_path: '/tmp/new.ts',
      content: 'fallbackWriteBodyZZZ\n',
    }) as unknown as Record<string, unknown>,
  )), AgentProvider.CLAUDE_CODE)

  it('renders the result-side structuredPatch when present', () => {
    const parsed = makeClaudeToolResultMessage({
      type: 'update',
      tool_name: 'Write',
      filePath: '/tmp/new.ts',
      structuredPatch: [
        { oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, lines: ['-resultWriteOld', '+resultWriteNew'] },
      ],
    }, 'Updated.')
    const { container } = renderClaudeToolResult(parsed, { sources: testMessageSources({ request: () => (writeToolUseParsed) }) })
    const text = container.textContent ?? ''
    expect(text).toContain('resultWriteOld')
    expect(text).toContain('resultWriteNew')
    expect(text).not.toContain('fallbackWriteBodyZZZ')
  })

  it('falls back to the linked Write tool_use content (new-file all-added case)', () => {
    // No structuredPatch on result, no oldString/newString — only filePath.
    const parsed = makeClaudeToolResultMessage({
      type: 'create',
      tool_name: 'Write',
      filePath: '/tmp/new.ts',
    }, 'Created.')
    const { container } = renderClaudeToolResult(parsed, { sources: testMessageSources({ request: () => (writeToolUseParsed) }) })
    const text = container.textContent ?? ''
    // The fallback all-added diff renders the new file content.
    expect(text).toContain('fallbackWriteBodyZZZ')
  })

  it('renders the error message and suppresses the fallback diff when is_error=true', () => {
    const errorContent = '<tool_use_error>File has not been read yet. Read it first before writing to it.</tool_use_error>'
    const parsed = makeClaudeToolResultMessage(undefined, errorContent, {
      isError: true,
      toolUseResultRaw: 'Error: File has not been read yet. Read it first before writing to it.',
    })
    const { container } = renderClaudeToolResult(parsed, { spanType: 'Write', sources: testMessageSources({ request: () => (writeToolUseParsed) }) })
    const text = container.textContent ?? ''
    expect(text).toContain('File has not been read yet')
    expect(text).not.toContain('fallbackWriteBodyZZZ')
    expect(text).toContain('Error')
  })
})

// ---------------------------------------------------------------------------
// Pi: the request states the change it asks for, which claims nothing about the file
// ---------------------------------------------------------------------------

describe('pi Edit/Write tool_use states its requested change', () => {
  it('edit tool_use shows the file, its statistics, and the change it asks for', () => {
    const { container } = renderPiToolUse('edit', {
      path: '/tmp/example.ts',
      edits: [{ oldText: 'piOldToolUseMarker', newText: 'piNewToolUseMarker' }],
    })
    const text = container.textContent ?? ''
    expect(text).toContain('example.ts')
    expect(text).toContain('+1')
    expect(text).toContain('-1')
    // The requested diff IS what a pending edit row states under the pair model.
    expect(text).toContain('piOldToolUseMarker')
    expect(text).toContain('piNewToolUseMarker')
  })

  it('write tool_use shows the file and the content it proposes', () => {
    const { container } = renderPiToolUse('write', {
      path: '/tmp/new.ts',
      content: 'piWriteToolUseMarker\n',
    })
    const text = container.textContent ?? ''
    expect(text).toContain('new.ts')
    expect(text).toContain('piWriteToolUseMarker')
  })
})

describe('pi Edit tool_result diff selection', () => {
  it('renders the result-side details.diff when present and ignores the tool_use fallback', () => {
    const resultDiff = [
      ' 1 keep',
      '-2 piResultOldMarker',
      '+2 piResultNewMarker',
    ].join('\n')
    const { container } = renderPiToolResult('edit', {
      content: [{ type: 'text', text: 'Modified.' }],
      details: { diff: resultDiff },
    }, {
      path: '/tmp/file.ts',
      edits: [{ oldText: 'piFallbackOldMarker', newText: 'piFallbackNewMarker' }],
    })
    const text = container.textContent ?? ''
    expect(text).toContain('piResultOldMarker')
    expect(text).toContain('piResultNewMarker')
    expect(text).not.toContain('piFallbackOldMarker')
    expect(text).not.toContain('piFallbackNewMarker')
    expect(text).not.toContain('Modified.')
  })

  it('falls back to linked edit tool_use diffs when the result has no diff', () => {
    const { container } = renderPiToolResult('edit', {
      content: [{ type: 'text', text: 'Modified.' }],
      details: {},
    }, {
      path: '/tmp/file.ts',
      edits: [{ oldText: 'piFallbackOldMarker', newText: 'piFallbackNewMarker' }],
    })
    const text = container.textContent ?? ''
    expect(text).toContain('piFallbackOldMarker')
    expect(text).toContain('piFallbackNewMarker')
    expect(text).not.toContain('Modified.')
  })

  it('renders error text instead of linked edit tool_use diffs when isError is true', () => {
    const { container } = renderPiToolResult('edit', {
      content: [{ type: 'text', text: 'Found 2 occurrences of edits[2]; oldText must be unique.' }],
      details: {},
    }, {
      path: '/tmp/file.ts',
      edits: [{ oldText: 'piFailedFallbackOldMarker', newText: 'piFailedFallbackNewMarker' }],
    }, true)
    const text = container.textContent ?? ''
    expect(text).toContain('Found 2 occurrences')
    expect(text).not.toContain('piFailedFallbackOldMarker')
    expect(text).not.toContain('piFailedFallbackNewMarker')
  })
})

describe('pi Write tool_result diff selection', () => {
  it('falls back to linked write tool_use content as an all-added diff', () => {
    const { container } = renderPiToolResult('write', {
      content: [{ type: 'text', text: 'Created.' }],
      details: {},
    }, {
      path: '/tmp/new.ts',
      content: 'piFallbackWriteBodyMarker\n',
    })
    const text = container.textContent ?? ''
    expect(text).toContain('piFallbackWriteBodyMarker')
    expect(text).not.toContain('Created.')
  })

  it('renders error text instead of linked write content when isError is true', () => {
    const { container } = renderPiToolResult('write', {
      content: [{ type: 'text', text: 'Refusing to overwrite existing file.' }],
      details: {},
    }, {
      path: '/tmp/new.ts',
      content: 'piFailedFallbackWriteBodyMarker\n',
    }, true)
    const text = container.textContent ?? ''
    expect(text).toContain('Refusing to overwrite existing file')
    expect(text).not.toContain('piFailedFallbackWriteBodyMarker')
  })
})

// ---------------------------------------------------------------------------
// Codex fileChange
// ---------------------------------------------------------------------------

function renderCodexFileChange(item: Record<string, unknown>, context?: MessageContentRenderContext) {
  const parsed = { item, threadId: 't1', turnId: 'r1' }
  const category: MessageCategory = { kind: 'tool_use' }
  const result = renderMessageContent(parsed, context, category, AgentProvider.CODEX)
  return render(() => result)
}

describe('codex fileChange routes through the shared diff component', () => {
  it('renders a simple add as an all-added diff', () => {
    const { container } = renderCodexFileChange({
      type: 'fileChange',
      status: 'completed',
      changes: [
        { path: '/tmp/added.ts', kind: 'add', diff: 'codexAddedContentMARKER\nsecondLine\n' },
      ],
    })
    const text = container.textContent ?? ''
    expect(text).toContain('codexAddedContentMARKER')
    expect(text).toContain('secondLine')
  })

  it('renders multiple completed entries via the shared diff body', () => {
    const { container } = renderCodexFileChange({
      type: 'fileChange',
      status: 'completed',
      changes: [
        {
          path: '/tmp/a.ts',
          kind: 'update',
          diff: '@@ -1,1 +1,1 @@\n-codexFileAOld\n+codexFileANew\n',
        },
        {
          path: '/tmp/b.ts',
          kind: 'update',
          diff: '@@ -1,1 +1,1 @@\n-codexFileBOld\n+codexFileBNew\n',
        },
      ],
    })
    const text = container.textContent ?? ''
    expect(text).toContain('codexFileAOld')
    expect(text).toContain('codexFileANew')
    expect(text).toContain('codexFileBOld')
    expect(text).toContain('codexFileBNew')
  })

  // A failed change never landed, so the row draws NO diff at all -- neither as a
  // body, which would read as an edit the file received, nor as a request. The
  // reason the call gives is the whole answer. `RequestedChangesBody` owns that rule
  // for every provider, so Codex and Claude no longer disagree about the same row.
  it('draws no diff for a failed fileChange', () => {
    const { container } = renderCodexFileChange({
      type: 'fileChange',
      status: 'failed',
      changes: [
        {
          path: '/tmp/failed.ts',
          kind: 'update',
          diff: '@@ -1,1 +1,1 @@\n-codexFailedOld\n+codexFailedNew\n',
        },
      ],
    })
    const text = container.textContent ?? ''
    expect(text).not.toContain('Requested changes')
    expect(text).not.toContain('codexFailedOld')
  })
})

// ---------------------------------------------------------------------------
// OpenCode tool_call_update
// ---------------------------------------------------------------------------

function renderOpenCodeUpdate(toolUse: Record<string, unknown>, context?: MessageContentRenderContext) {
  const category: MessageCategory = { kind: 'tool_use' }
  const result = renderMessageContent(toolUse, context, category, AgentProvider.OPENCODE)
  return render(() => result)
}

describe('opencode tool_call_update diff selection', () => {
  // `rawInput` states the file the call ASKED to change, which the row heads itself
  // with at every state of the call (invariant I7). It carries no replacement text
  // here, so it synthesizes no diff of its own and the content array stays the one
  // source of the body below.
  it('renders the diff embedded in the update content array', () => {
    const { container } = renderOpenCodeUpdate({
      sessionUpdate: 'tool_call_update',
      kind: 'edit',
      status: 'completed',
      title: 'Edit /tmp/a.ts',
      rawInput: { filePath: '/tmp/a.ts' },
      content: [
        { type: 'diff', path: '/tmp/a.ts', oldText: 'opencodeContentOldX', newText: 'opencodeContentNewX' },
      ],
    })
    const text = container.textContent ?? ''
    expect(text).toContain('opencodeContentOldX')
    expect(text).toContain('opencodeContentNewX')
  })

  it('falls back to a diff synthesized from rawInput when content has no diff', () => {
    const { container } = renderOpenCodeUpdate({
      sessionUpdate: 'tool_call_update',
      kind: 'edit',
      status: 'completed',
      title: 'Edit /tmp/b.ts',
      rawInput: {
        filePath: '/tmp/b.ts',
        oldText: 'opencodeRawOldY',
        newText: 'opencodeRawNewY',
      },
      content: [],
    })
    const text = container.textContent ?? ''
    expect(text).toContain('opencodeRawOldY')
    expect(text).toContain('opencodeRawNewY')
  })

  it('renders text output (and no diff) when neither content nor rawInput carry diff data', () => {
    const { container } = renderOpenCodeUpdate({
      sessionUpdate: 'tool_call_update',
      kind: 'execute',
      status: 'completed',
      title: 'Run something',
      rawInput: { command: 'echo hi' },
      content: [
        { type: 'content', content: { text: 'opencodePlainOutputZ' } },
      ],
    })
    const text = container.textContent ?? ''
    expect(text).toContain('opencodePlainOutputZ')
  })

  it('renders failure output and draws no diff when status is failed', () => {
    const { container } = renderOpenCodeUpdate({
      sessionUpdate: 'tool_call_update',
      kind: 'edit',
      status: 'failed',
      title: 'Edit /tmp/failed.ts',
      rawInput: {
        filePath: '/tmp/failed.ts',
        oldText: 'opencodeFailedRawOld',
        newText: 'opencodeFailedRawNew',
      },
      content: [
        { type: 'content', content: { text: 'Could not apply patch.' } },
      ],
    })
    const text = container.textContent ?? ''
    expect(text).toContain('Could not apply patch')
    // The patch never applied, so neither side of it draws.
    expect(text).not.toContain('Requested changes')
    expect(text).not.toContain('opencodeFailedRawOld')
    expect(text).not.toContain('opencodeFailedRawNew')
  })

  // Regression: an earlier cleanup wrote `!effectiveDiff() !== null`, which
  // parses as `(!effectiveDiff()) !== null` and is always true, so the
  // text-output branch rendered alongside the diff. The fix gates on
  // `effectiveDiff() === null`.
  it('does not duplicate text output below the diff when both are present', () => {
    const { container } = renderOpenCodeUpdate({
      sessionUpdate: 'tool_call_update',
      kind: 'edit',
      status: 'completed',
      title: 'Edit /tmp/c.ts',
      rawInput: { filePath: '/tmp/c.ts' },
      content: [
        { type: 'diff', path: '/tmp/c.ts', oldText: 'duplicateGuardOldA', newText: 'duplicateGuardNewA' },
        { type: 'content', content: { text: 'duplicateGuardTextB' } },
      ],
    })
    const text = container.textContent ?? ''
    expect(text).toContain('duplicateGuardOldA')
    expect(text).toContain('duplicateGuardNewA')
    expect(text).not.toContain('duplicateGuardTextB')
  })

  // Regression: the span merge once told the row's own result side from the
  // current frame by OBJECT IDENTITY, but the resolver hands the result side
  // back as a re-parse -- a distinct object for the same message. A finished
  // row then folded the result frame in as its own request, merging no request
  // at all, and the edit fell to the uncategorized card: its words drew, the
  // requested change beside them did not. The finished flag tells the sides
  // apart, so this pair must draw both halves however many times the message
  // was parsed.
  it('keeps the requested edit beside reported words when the result side is a re-parse of the row', () => {
    const parse = (content: Record<string, unknown>) => resolveMessageForRendering(parseMessageContent(makeFakeMessage(content)), AgentProvider.OPENCODE)
    const request = parse({
      sessionUpdate: 'tool_call',
      toolCallId: 'unconfirmed-edit',
      kind: 'edit',
      status: 'pending',
      title: 'edit',
      rawInput: { filePath: '/project/example.ts', oldString: 'requestedBefore', newString: 'requestedAfter' },
    })
    const resultContent = {
      sessionUpdate: 'tool_call_update',
      toolCallId: 'unconfirmed-edit',
      status: 'completed',
      content: [{ type: 'content', content: { type: 'text', text: 'No file changes occurred.' } }],
    }
    const { container } = render(() => renderMessageContent(
      resultContent,
      { sources: testMessageSources({ current: () => parse(resultContent), request: () => request, result: () => parse(resultContent), role: () => 'result' }) },
      { kind: 'tool_use' } as MessageCategory,
      AgentProvider.OPENCODE,
    ))
    const text = container.textContent ?? ''
    expect(text).toContain('No file changes occurred.')
    expect(text).toContain('Requested changes')
    expect(text).toContain('requestedBefore')
    expect(text).toContain('requestedAfter')
  })
})
