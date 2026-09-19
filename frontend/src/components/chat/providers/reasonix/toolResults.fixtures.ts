import type { ToolKind } from '~/components/chat/ir/toolKind'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ToolFailureFixture, ToolResultCheck, ToolResultFixture } from '~/test-support/toolVocabulary'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../testUtils'

const CALL = 'rx-1'

/** A finished Reasonix update, with the request that states the tool. */
function rxUpdate(frame: Record<string, unknown>, request: Record<string, unknown>): ToolResultFixture {
  return {
    payload: { sessionUpdate: 'tool_call_update', toolCallId: CALL, status: 'completed', ...frame },
    options: {
      spanType: 'tool_call_update',
      request: input({ sessionUpdate: 'tool_call', toolCallId: CALL, status: 'pending', ...request }) as ParsedMessageContent,
    },
  }
}

function text(value: string) {
  return [{ type: 'content', content: { type: 'text', text: value } }]
}

const FIXTURES: Readonly<Record<string, ToolResultFixture>> = {
  read_file: rxUpdate({ content: text('one\ntwo\n') }, { title: 'read_file', kind: 'read', rawInput: { path: '/p/a.ts' } }),
  view_image: rxUpdate({
    content: [
      { type: 'content', content: { type: 'text', text: 'The picture at /p/dot.png.' } },
      { type: 'content', content: { type: 'image', data: 'aGk=', mimeType: 'image/png' } },
    ],
  }, { title: 'view_image', kind: 'read', rawInput: { path: '/p/dot.png' } }),
  glob: rxUpdate({ content: text('a.py\nb.py\n') }, { title: 'glob', kind: 'search', rawInput: { pattern: '*.py' } }),
  grep: rxUpdate({ content: text('/p/a.ts:3:needle\n/p/b.ts:9:needle\n') }, { title: 'grep', kind: 'search', rawInput: { pattern: 'needle' } }),
  ls: rxUpdate({ content: text('a.ts\t42\nsrc/\n') }, { title: 'ls', kind: 'read', rawInput: { path: '/project' } }),
  edit_file: rxUpdate(
    { content: text('edited /p/file.ts\nActual replacement receipt after write:\n@@ replacement 1 of 1 (1 occurrence(s)) @@\n-actualBefore\n+actualAfter\n') },
    { title: 'edit_file', kind: 'edit', rawInput: { path: '/p/file.ts', old_string: 'requestedBefore', new_string: 'requestedAfter' } },
  ),
  multi_edit: rxUpdate({ content: text('Applied edits') }, { title: 'multi_edit', kind: 'edit', rawInput: { path: '/p/file.ts', edits: [{ old_string: 'firstBefore', new_string: 'firstAfter' }, { old_string: 'secondBefore', new_string: 'secondAfter' }] } }),
  write_file: rxUpdate({ content: text('Wrote /p/n.ts') }, { title: 'write_file', kind: 'write', rawInput: { path: '/p/n.ts', content: 'written()' } }),
  move_file: rxUpdate({ content: text('moved') }, { title: 'move_file', kind: 'edit', rawInput: { source_path: '/p/old.ts', destination_path: '/p/new.ts' } }),
  delete_range: rxUpdate({ content: text('--- a/p/file.ts\n+++ b/p/file.ts\n@@ -4,3 +4,1 @@\n-removedFirst\n-removedSecond\n remaining\n') }, { title: 'delete_range', kind: 'delete', rawInput: { path: '/p/file.ts', start_anchor: 'delete', end_anchor: 'end' } }),
  bash: rxUpdate({ content: text('hello\n') }, { title: 'bash', kind: 'execute', rawInput: { command: 'printf hello', description: 'Say hello' } }),
  web_fetch: rxUpdate({ content: text('# Example\n\n**Fetched body**') }, { title: 'web_fetch', kind: 'fetch', rawInput: { url: 'https://example.com' } }),
  task: rxUpdate({ content: text('Subagent outcome: status=completed retryable=false\n\nFinal answer:\n**Findings**') }, { title: 'task', kind: 'other', rawInput: { description: 'Inspect sample', prompt: 'Read it' } }),
  todo_write: rxUpdate({ content: text('Todos updated') }, { title: 'todo_write', kind: 'edit', rawInput: { todos: [{ content: 'Inspect code', status: 'in_progress' }] } }),
  lookup: rxUpdate({ content: text('probe answered') }, { title: 'mcp__probe__lookup', kind: 'other', rawInput: { query: 'needle' } }),
}

/**
 * The sentence every failed fixture carries.
 *
 * Synthetic on purpose. Reasonix's own error wording is not confirmable from this
 * repository, and the guard asks about the LADDER -- the outcome word, the brand, the
 * kind and the request -- rather than about any provider's choice of words.
 */
const ERROR_TEXT = 'The tool reported an error.'

/**
 * The FAILED frame of the call one successful fixture already states.
 *
 * It keeps the frame's identity and the request that states the tool. These fields describe the TOOL and never
 * the outcome -- and replaces the answer with the reason. Everything a successful call
 * left behind is gone: a call that failed computed no `rawOutput`, no diff and no
 * display record.
 *
 * The request half comes from the successful fixture rather than from a second copy of
 * the request. The two frames then describe ONE call, which is what lets the ladder
 * assert that a failure keeps the kind, the tool and the request of its success.
 */
function failed(kind: ToolKind, name: string, status: ToolFailureFixture['status'] = 'failed'): ToolFailureFixture {
  // Every failure pairs by name with a fixture above, so the read is guarded for the type alone.
  const fixture = FIXTURES[name]
  return {
    payload: { sessionUpdate: 'tool_call_update', toolCallId: CALL, status: 'failed', content: text(ERROR_TEXT) },
    ...(fixture?.options !== undefined ? { options: fixture.options } : {}),
    kind,
    name,
    status,
  }
}

export const REASONIX_TOOL_RESULTS: ToolResultCheck = {
  provider: AgentProvider.REASONIX,
  fixtures: FIXTURES,
  failures: [
    failed('read', 'read_file'),
    failed('glob', 'glob'),
    failed('grep', 'grep'),
    failed('list', 'ls'),
    failed('edit', 'edit_file'),
    failed('write', 'write_file'),
    failed('move', 'move_file'),
    failed('delete', 'delete_range'),
    failed('execute', 'bash'),
    failed('fetch', 'web_fetch'),
    failed('agent', 'task'),
    failed('todo', 'todo_write'),
    failed('mcp', 'lookup'),
  ],
  noFailure: {},
  noResult: {},
  unparsed: {
    read_file: 'Reasonix prints the body without line numbers, and the shared reader takes it as the words beside the path.',
    multi_edit: 'Reasonix states a multi-edit in words; the requested replacements draw beside them.',
    write_file: 'Reasonix states a completed write in words; the requested content draws beside them.',
    view_image: 'The sentence beside the picture is not the numbered file body the read body draws.',
    move_file: 'The two paths are the REQUEST, which the row draws at every state of the call; the daemon\'s own sentence stays beside them.',
  },
}
