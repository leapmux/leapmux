import type { ToolKind } from '~/components/chat/ir/toolKind'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ToolFailureFixture, ToolResultCheck, ToolResultFixture } from '~/test-support/toolVocabulary'
import { CURSOR_METHOD, CURSOR_SUPPLEMENT } from '~/generated/contracts/cursor-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../testUtils'

const CALL = 'cursor-1'

/** A finished Cursor update, with its opener and the record the worker stored on it. */
function cursorUpdate(frame: Record<string, unknown>, opener: Record<string, unknown>, supplemental?: Record<string, unknown>): ToolResultFixture {
  const payload = { sessionUpdate: 'tool_call_update', toolCallId: CALL, status: 'completed', ...frame }
  return {
    payload,
    options: {
      spanType: 'tool_call_update',
      request: input({ sessionUpdate: 'tool_call', toolCallId: CALL, status: 'pending', ...opener }) as ParsedMessageContent,
      ...(supplemental ? { supplementalContent: { sessionUpdate: payload.sessionUpdate, toolCallId: CALL, status: payload.status, ...supplemental } } : {}),
    },
  }
}

/** The native record a stored tool result keeps under `providerOptions.cursor`. */
function nativeRecord(success: Record<string, unknown>): Record<string, unknown> {
  return { providerOptions: { cursor: { highLevelToolCallResult: { output: { success } } } } }
}

/** One saved tool result, as the worker stores it on the row. */
function savedResult(toolName: string, result: unknown, extra: Record<string, unknown> = {}): Record<string, unknown> {
  return { rawOutput: { content: [{ type: 'tool-result', toolCallId: CALL, toolName, result }], ...extra } }
}

const FIXTURES: Readonly<Record<string, ToolResultFixture>> = {
  task: cursorUpdate(
    { kind: 'other', rawInput: { _toolName: 'task', description: 'Inspect sample' }, rawOutput: { durationMs: 1000, isBackground: false } },
    { kind: 'other', rawInput: { _toolName: 'task', description: 'Inspect sample', prompt: 'Read the file' } },
    savedResult('Task', 'Native final report', nativeRecord({ conversationSteps: [{ assistantMessage: { text: '- Read **sample.py**' } }], agentId: 'child-1', durationMs: '1000' })),
  ),
  createPlan: cursorUpdate(
    { kind: 'other', title: 'Create Plan', rawInput: { _toolName: 'createPlan' } },
    { kind: 'other', title: 'Create Plan', rawInput: { _toolName: 'createPlan' } },
    { rawInput: { _toolName: 'createPlan', name: 'Add CHANGELOG.md', plan: '# Add CHANGELOG.md\n\n## Steps\n\n- Seed an Unreleased section.' } },
  ),
  updateTodos: cursorUpdate(
    { kind: 'other', title: 'Update TODOs', rawInput: { _toolName: 'updateTodos' } },
    { kind: 'other', title: 'Update TODOs', rawInput: { _toolName: 'updateTodos' } },
    { [CURSOR_SUPPLEMENT.Extension]: { method: CURSOR_METHOD.UpdateTodos, params: { toolCallId: CALL, todos: [{ id: '1', content: 'Create a.txt', status: 'in_progress' }] } } },
  ),
  generateImage: cursorUpdate(
    { kind: 'other', rawInput: { _toolName: 'generateImage', description: 'A cat' } },
    { kind: 'other', rawInput: { _toolName: 'generateImage', description: 'A cat' } },
    { [CURSOR_SUPPLEMENT.Extension]: { method: CURSOR_METHOD.GenerateImage, params: { toolCallId: CALL, description: 'A cat', filePath: '/tmp/cat.png' } } },
  ),
  askQuestion: cursorUpdate(
    { kind: 'think', title: 'Pick a parser', rawInput: { _toolName: 'askQuestion', title: 'Pick a parser', questions: [{ prompt: 'Which one?', options: [{ id: 'a', label: 'Recursive descent' }] }] } },
    { kind: 'think', title: 'Pick a parser', rawInput: { _toolName: 'askQuestion', title: 'Pick a parser', questions: [{ prompt: 'Which one?', options: [{ id: 'a', label: 'Recursive descent' }] }] } },
  ),
  grep: cursorUpdate({ kind: 'search', title: 'grep "needle"', rawInput: { pattern: 'needle' }, rawOutput: { totalMatches: 1 } }, { kind: 'search', title: 'grep "needle"', rawInput: { pattern: 'needle' } }),
  bash: cursorUpdate({ kind: 'execute', rawInput: { command: 'python3 sample.py' }, rawOutput: { stdout: 'answer = 42\n', exitCode: 0 } }, { kind: 'execute', rawInput: { command: 'python3 sample.py' } }),
  read: cursorUpdate({ kind: 'read', rawInput: { path: '/project/sample.py' }, rawOutput: { content: 'answer = 42\n' }, locations: [{ path: '/project/sample.py', line: 7 }] }, { kind: 'read', rawInput: { path: '/project/sample.py' } }),
  str_replace: cursorUpdate(
    { kind: 'edit', rawInput: { path: '/project/sample.py', old_string: '41', new_string: '42' }, content: [{ type: 'diff', path: '/project/sample.py', oldText: '41', newText: '42' }] },
    { kind: 'edit', rawInput: { path: '/project/sample.py', old_string: '41', new_string: '42' } },
    savedResult('StrReplace', 'Saved', nativeRecord({ path: '/project/sample.py', beforeFullFileContent: 'answer = 41\n', afterFullFileContent: 'answer = 42\n' })),
  ),
  mcp_probe_echo: cursorUpdate(
    { kind: 'other', title: 'mcp_probe_echo', rawInput: { providerIdentifier: 'probe', toolName: 'echo' } },
    { kind: 'other', title: 'mcp_probe_echo', rawInput: { providerIdentifier: 'probe', toolName: 'echo' } },
    savedResult('mcp_probe_echo', { count: 0 }, nativeRecord({ content: [{ text: { text: 'probe answered' } }] })),
  ),
}

/**
 * The sentence every failed fixture carries.
 *
 * Synthetic on purpose. Cursor's own error wording is not confirmable from this
 * repository, and the guard asks about the LADDER -- the outcome word, the brand, the
 * kind and the request -- rather than about any provider's choice of words.
 */
const ERROR_TEXT = 'The tool reported an error.'

/**
 * The FAILED frame of the call one successful fixture already states.
 *
 * It keeps the frame's identity -- the call id and the wire kind, which describe the
 * TOOL and never the outcome -- and replaces the answer with the reason. Every record
 * a successful call left behind is gone: a failed call computed no `rawOutput`, and
 * the worker stored no native result beside it.
 */
function failed(kind: ToolKind, name: string, status: ToolFailureFixture['status'] = 'failed'): ToolFailureFixture {
  // Every failure pairs by name with a fixture above, so the reads below are guarded
  // for the type alone.
  const fixture = FIXTURES[name]
  const wireKind = fixture?.payload.kind
  const request = fixture?.options?.request
  return {
    payload: { sessionUpdate: 'tool_call_update', toolCallId: CALL, ...(wireKind === undefined ? {} : { kind: wireKind }), status: 'failed', content: [{ type: 'content', content: { type: 'text', text: ERROR_TEXT } }] },
    options: { spanType: 'tool_call_update', ...(request !== undefined ? { request } : {}) },
    kind,
    name,
    status,
  }
}

export const CURSOR_TOOL_RESULTS: ToolResultCheck = {
  provider: AgentProvider.CURSOR,
  fixtures: FIXTURES,
  failures: [
    failed('agent', 'task'),
    failed('report', 'createPlan'),
    failed('todo', 'updateTodos'),
    failed('image', 'generateImage'),
    failed('question', 'askQuestion'),
    failed('grep', 'grep'),
    failed('execute', 'bash'),
    failed('read', 'read'),
    failed('edit', 'str_replace'),
    failed('mcp', 'mcp_probe_echo'),
  ],
  noFailure: {},
  noResult: {},
  unparsed: {},
}
