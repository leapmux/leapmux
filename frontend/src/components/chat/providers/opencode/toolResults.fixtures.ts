import type { ToolKind } from '~/components/chat/model/toolKind'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ToolFailureFixture, ToolResultCheck, ToolResultFixture } from '~/test-support/toolVocabulary'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../testUtils'

const CALL = 'oc-1'

/** A finished update whose paired request identifies the tool and the arguments. */
function update(rawInput: Record<string, unknown>, result: Record<string, unknown> = {}, title = 'bash'): ToolResultFixture {
  return {
    payload: { sessionUpdate: 'tool_call_update', toolCallId: CALL, status: 'completed', ...result },
    options: {
      spanType: 'tool_call_update',
      request: input({ sessionUpdate: 'tool_call', toolCallId: CALL, status: 'pending', title, rawInput }) as ParsedMessageContent,
    },
  }
}

const display = (display: Record<string, unknown>) => ({ rawOutput: { metadata: { display } } })

const FIXTURES: Readonly<Record<string, ToolResultFixture>> = {
  bash: update({ command: 'ls' }, { kind: 'execute', content: text('a.ts') }),
  read_file: update({ filePath: '/p/a.ts', offset: 1 }, { kind: 'read', ...display({ type: 'file', path: '/p/a.ts', text: 'alpha', lineStart: 1, totalLines: 1 }) }, 'read'),
  list: update({ path: '/p' }, { kind: 'read', ...display({ type: 'directory', path: '/p', entries: ['a.ts'] }) }, 'ls'),
  edit: update({ filePath: '/p/a.ts', oldString: 'x', newY: 'y' }, { kind: 'edit', content: [{ type: 'diff', path: '/p/a.ts', oldText: 'x', newText: 'y' }] }, 'edit'),
  write: update({ filePath: '/p/n.ts', content: 'new' }, { kind: 'edit', ...display({ type: 'file', path: '/p/n.ts', text: 'new' }) }, 'write'),
  apply_patch: update({ filePath: '/p/a.ts', oldString: 'before', newString: 'after' }, { kind: 'edit', content: [{ type: 'diff', path: '/p/a.ts', oldText: 'before', newText: 'after' }] }, 'apply_patch'),
  glob: update({ pattern: '*.ts' }, { kind: 'search', rawOutput: { metadata: { count: 1 } }, content: text('a.ts') }, 'glob'),
  grep: update({ pattern: 'needle' }, { kind: 'search', rawOutput: { metadata: { matches: 1 } }, content: text('a.ts:1:needle') }, 'grep'),
  task: update({ description: 'Probe', prompt: 'Run.' }, { kind: 'think', content: text('<task id="t1" state="completed">\n<task_result>\ndone\n</task_result>\n</task>') }, 'task'),
  todowrite: update({ todos: [{ content: 'One', status: 'pending' }] }, { kind: 'other', content: text('Saved') }, 'todowrite'),
  question: update({ questions: [{ header: 'Pick', question: 'Which?', options: [] }] }, { kind: 'other', content: text('answered') }, 'question'),
  webfetch: update({ url: 'https://example.com' }, { kind: 'fetch', content: text('# page') }, 'webfetch'),
  probe_echo: update({ query: 'needle' }, { kind: 'other', content: [{ type: 'content', content: { type: 'text', text: 'found' } }] }, 'probe_echo'),
}

/**
 * The sentence every failed fixture carries.
 *
 * Synthetic on purpose. OpenCode's own error wording is not confirmable from this
 * repository, and the guard asks about the LADDER -- the outcome word, the brand, the
 * kind and the request -- rather than about any provider's choice of words.
 */
const ERROR_TEXT = 'The tool reported an error.'

/**
 * The FAILED frame of the call one successful fixture already states.
 *
 * It keeps the frame's identity -- the call id and the wire kind, which describe the TOOL and never
 * the outcome -- and replaces the answer with the reason. Everything a successful call
 * left behind is gone: a call that failed computed no `rawOutput`, no diff and no
 * display record.
 *
 * The request half comes from the successful fixture rather than from a second copy of
 * the request. The two frames then describe ONE call, which is what lets the ladder
 * assert that a failure keeps the kind, the tool and the request of its success.
 */
function failed(kind: ToolKind, name: string, status: ToolFailureFixture['status'] = 'failed'): ToolFailureFixture {
  // Every failure pairs by name with a fixture above, so the reads are guarded for the type alone.
  const fixture = FIXTURES[name]
  return {
    payload: { sessionUpdate: 'tool_call_update', toolCallId: CALL, kind: fixture?.payload.kind, status: 'failed', content: text(ERROR_TEXT) },
    ...(fixture?.options !== undefined ? { options: fixture.options } : {}),
    kind,
    name,
    status,
  }
}

export const OPENCODE_TOOL_RESULTS: ToolResultCheck = {
  provider: AgentProvider.OPENCODE,
  fixtures: FIXTURES,
  failures: [
    failed('execute', 'bash'),
    failed('read', 'read_file'),
    // `ls` is a read frame, and only the display metadata of an ANSWER says that what
    // it read is a directory. A call that failed carries none, so it stays a read.
    failed('read', 'list'),
    failed('edit', 'edit'),
    failed('write', 'write'),
    failed('glob', 'glob'),
    // The tool NAME states this search is a grep, and every state of the call keeps it.
    // The successful fixture carries a body this build cannot read as OpenCode's own
    // grep format, so it takes the counter-only reading -- which used to answer the
    // wider `search` kind and left one call drawing two different rows.
    failed('grep', 'grep'),
    failed('agent', 'task'),
    failed('todo', 'todowrite'),
    failed('question', 'question'),
    failed('fetch', 'webfetch'),
    failed('mcp', 'probe_echo'),
  ],
  noFailure: {
    // The one kind the answer decides. OpenCode's `read` runs two operations behind one
    // registry id -- a file body and a directory listing -- and only the display
    // metadata of a SUCCESSFUL answer says which one ran. A call that failed carries
    // none, so it keeps the read kind its own frame states.
    list: 'A directory listing arrives in the display metadata of a SUCCESSFUL answer, so a call that failed stays on the read kind its own frame states.',
  },
  noResult: {},
  unparsed: {},
}

function text(value: string) {
  return [{ type: 'content', content: { type: 'text', text: value } }]
}
