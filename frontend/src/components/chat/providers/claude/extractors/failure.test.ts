import type { ToolKind } from '../../../model/toolKind'
import type { ClaudeToolRow } from './toolCommon'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { invariantViolations } from '~/test-support/toolVocabulary'
import { typedResult } from '../../../model/toolCall'
import { CLAUDE_TOOL_NAMES } from '../toolNames'
import { claudeToolFailureResult } from './failure'
import '~/components/chat/providers'

/** The reason the command line interface sends for a path that is not there. */
const REASON = 'File does not exist.'

/** One Claude RESULT row, built from fields rather than from an envelope. */
function row(overrides: Partial<ClaudeToolRow> = {}): ClaudeToolRow {
  return {
    id: 'tu_1',
    role: 'result',
    toolName: CLAUDE_TOOL_NAMES.GREP,
    input: {},
    toolUseResult: undefined,
    resultContent: REASON,
    rawResultContent: REASON,
    images: [],
    isError: undefined,
    ...overrides,
  }
}

/**
 * A `tool_result` frame the tool marked failed.
 *
 * No `tool_use_result` rides beside it, which is the whole point: a failed Claude call
 * carries its reason and nothing else, so every kind's parser sees the error sentence
 * where it expects the kind's own payload.
 */
function failedFrame(content = REASON): Record<string, unknown> {
  return {
    type: 'user',
    message: { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'r1', content, is_error: true }] },
  }
}

/** The `tool_use` side beside it, which the result row reads its arguments from. */
function requestSide(name: string, args: Record<string, unknown>): ParsedMessageContent {
  return {
    wrapper: null,
    topLevel: null,
    parentObject: {
      type: 'assistant',
      message: { role: 'assistant', content: [{ type: 'tool_use', id: 'r1', name, input: args }] },
    },
    rawText: '',
    supplementalContent: undefined,
    messageMetadata: undefined,
  }
}

/** The one call a failed frame of `name` extracts to, the way a mounted row reads it. */
function failedCall(name: string, args: Record<string, unknown>, content = REASON) {
  const call = providerToolCall(AgentProvider.CLAUDE_CODE, failedFrame(content), {
    request: requestSide(name, args),
    spanType: name,
  })
  expect(call, name).not.toBeNull()
  return call!
}

/**
 * One case per kind whose answer a failed call used to be read as.
 *
 * The list is the ladder's coverage, so a kind added to `claudeSpec` without a
 * failure rung leaves a hole here that a reader can see.
 */
const CASES: { name: string, kind: ToolKind, args: Record<string, unknown> }[] = [
  { name: CLAUDE_TOOL_NAMES.GREP, kind: 'grep', args: { pattern: 'needle' } },
  { name: CLAUDE_TOOL_NAMES.GLOB, kind: 'glob', args: { pattern: '**/*.ts' } },
  { name: CLAUDE_TOOL_NAMES.READ, kind: 'read', args: { file_path: '/p/a.ts' } },
  { name: CLAUDE_TOOL_NAMES.WEB_FETCH, kind: 'fetch', args: { url: 'https://example.com' } },
  { name: CLAUDE_TOOL_NAMES.WEB_SEARCH, kind: 'web_search', args: { query: 'q' } },
  { name: CLAUDE_TOOL_NAMES.LIST_AGENTS, kind: 'agents', args: {} },
  { name: CLAUDE_TOOL_NAMES.SKILL, kind: 'skill', args: { skill: 'deep-review' } },
  { name: CLAUDE_TOOL_NAMES.SLEEP, kind: 'wait', args: { durationMs: 1000 } },
  { name: CLAUDE_TOOL_NAMES.STRUCTURED_OUTPUT, kind: 'report', args: { summary: 'done' } },
  { name: CLAUDE_TOOL_NAMES.SEND_MESSAGE, kind: 'message', args: { to: 'agent-2', message: 'hi' } },
  { name: CLAUDE_TOOL_NAMES.LIST_MCP_RESOURCES, kind: 'list', args: { server: 'files' } },
  { name: CLAUDE_TOOL_NAMES.REMOTE_TRIGGER, kind: 'trigger', args: { action: 'create' } },
  { name: CLAUDE_TOOL_NAMES.TASK_OUTPUT, kind: 'task', args: { task_id: 't-1' } },
  { name: CLAUDE_TOOL_NAMES.TASK_STOP, kind: 'task', args: { task_id: 't-1' } },
]

describe('claudeToolFailureResult', () => {
  it('states the reason alone for a row the tool marked failed', () => {
    expect(claudeToolFailureResult(row({ isError: true }))).toStrictEqual({ failure: true, text: REASON })
  })

  // An empty reason still takes the failed brand. `unparsedResult` is the other
  // spelling, and it claims the call COMPLETED -- which invariant I4 rejects under the
  // failed status such a row carries.
  it('keeps the failed brand for a reason with no words in it', () => {
    expect(claudeToolFailureResult(row({ isError: true, resultContent: '' }))).toStrictEqual({ failure: true, text: '' })
  })

  it('answers undefined for a row that did not fail and for one that has not answered', () => {
    expect(claudeToolFailureResult(undefined)).toBeUndefined()
    expect(claudeToolFailureResult(row({ isError: false }))).toBeUndefined()
    expect(claudeToolFailureResult(row({ isError: undefined }))).toBeUndefined()
  })
})

/**
 * Every kind answers the reason, and nothing else, for a call the tool failed.
 *
 * The parser of each kind reads the error SENTENCE as its own data without this rung.
 * Grep and Glob were the worst of them: the sentence fell to the subagent parser, which
 * classifies a line it cannot read as content as a FILE NAME, so the row drew the
 * summary "Found 1 file" over a file list holding "File does not exist.".
 */
describe('claudeSpec failure rung', () => {
  it.each(CASES)('states the reason alone for a failed $name', ({ name, kind, args }) => {
    const call = failedCall(name, args)
    expect(call.kind).toBe(kind)
    expect(call.status).toBe('failed')
    // `toStrictEqual`, so the assertion also pins that no `format` key rides along:
    // the markdown-formatted kinds drew the reason as markup through exactly that key.
    expect(call.result).toStrictEqual({ failure: true, text: REASON })
  })

  it.each(CASES)('holds every call invariant for a failed $name', ({ name, args }) => {
    expect(invariantViolations(failedCall(name, args))).toStrictEqual([])
  })

  // `typedResult` is what a body renderer reads. Undefined is what makes it draw the
  // plain reason instead of the kind's own body -- the file list, the fetched page,
  // the file viewer.
  it.each(CASES)('hands the body renderer no payload for a failed $name', ({ name, args }) => {
    expect(typedResult(failedCall(name, args))).toBeUndefined()
  })

  // The exact reason a failed search used to draw as a hit, kept as its own case: the
  // sentence holds a `.` and no `:`, so the raw parser reads it as a path.
  it('draws no file list for a search the tool failed', () => {
    const call = failedCall(CLAUDE_TOOL_NAMES.GREP, { pattern: 'needle' })
    expect(call.result).toStrictEqual({ failure: true, text: REASON })
    // `SearchResultBody` reads its file list and its "Found N files" summary from this
    // payload alone. Undefined is what stops the row drawing the reason as a hit.
    expect(typedResult(call)).toBeUndefined()
  })

  // A reason that holds markup is the case the markdown-drawn kinds break on: `agents`
  // renders its listing as markdown, and `fetch` renders the fetched page the same way.
  it.each([CLAUDE_TOOL_NAMES.LIST_AGENTS, CLAUDE_TOOL_NAMES.WEB_FETCH])('states a reason holding markup verbatim for %s', (name) => {
    const reason = '# Denied\n\n*no such agent*'
    const call = failedCall(name, {}, reason)
    expect(call.result).toStrictEqual({ failure: true, text: reason })
  })
})
