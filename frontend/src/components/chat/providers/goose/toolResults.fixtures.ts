import type { ToolKind } from '~/components/chat/model/toolKind'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ToolFailureFixture, ToolResultCheck, ToolResultFixture } from '~/test-support/toolVocabulary'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../testUtils'

const CALL = 'goose-1'

/** A finished Goose update, with its request and the `_meta` name that both sides carry. */
function gooseUpdate(frame: Record<string, unknown>, request: Record<string, unknown>): ToolResultFixture {
  return {
    payload: { sessionUpdate: 'tool_call_update', toolCallId: CALL, status: 'completed', ...frame },
    options: {
      spanType: 'tool_call_update',
      request: input({ sessionUpdate: 'tool_call', toolCallId: CALL, status: 'pending', kind: 'other', ...request }) as ParsedMessageContent,
    },
  }
}

function text(value: string) {
  return [{ type: 'content', content: { type: 'text', text: value } }]
}

const FIXTURES: Readonly<Record<string, ToolResultFixture>> = {
  shell: gooseUpdate(
    { rawInput: { command: 'printf hi', description: 'Say hi' }, rawOutput: { stdout: 'hi\n', exit_code: 0 }, _meta: { goose: { toolCall: { toolName: 'developer__shell', extensionName: 'developer' } } } },
    { title: 'shell', rawInput: { command: 'printf hi', description: 'Say hi' }, _meta: { goose: { toolCall: { toolName: 'developer__shell', extensionName: 'developer' } } } },
  ),
  edit: gooseUpdate(
    { content: [{ type: 'content', content: { type: 'text', text: 'Edited sample.py (1 lines -> 1 lines)' } }], _meta: { goose: { toolCall: { toolName: 'developer__edit', extensionName: 'developer' } } } },
    { title: 'edit · sample.py', rawInput: { path: 'sample.py', before: 'answer = 41', after: 'answer = 42' }, _meta: { goose: { toolCall: { toolName: 'developer__edit', extensionName: 'developer' } } } },
  ),
  write: gooseUpdate(
    { content: text('Wrote sample.py'), _meta: { goose: { toolCall: { toolName: 'developer__write', extensionName: 'developer' } } } },
    { rawInput: { path: 'sample.py', content: 'written()' }, _meta: { goose: { toolCall: { toolName: 'developer__write', extensionName: 'developer' } } } },
  ),
  read: gooseUpdate(
    { content: text('answer = 42\n'), _meta: { goose: { toolCall: { toolName: 'developer__read', extensionName: 'developer' } } } },
    { rawInput: { path: 'sample.py', line: 7 }, _meta: { goose: { toolCall: { toolName: 'developer__read', extensionName: 'developer' } } } },
  ),
  read_image: gooseUpdate(
    {
      content: [
        { type: 'content', content: { type: 'text', text: 'Loaded image from /repo/dot.png (70 bytes, image/png, 1x1).' } },
        { type: 'content', content: { type: 'image', data: 'aGk=', mimeType: 'image/png' } },
      ],
      rawOutput: { source: '/repo/dot.png', width: 1, height: 1 },
      _meta: { goose: { toolCall: { toolName: 'developer__read_image', extensionName: 'developer' } } },
    },
    { rawInput: { source: '/repo/dot.png' }, _meta: { goose: { toolCall: { toolName: 'developer__read_image', extensionName: 'developer' } } } },
  ),
  tree: gooseUpdate(
    { content: text('sample.py (10 lines)\ninner/ (2 files)'), _meta: { goose: { toolCall: { toolName: 'developer__tree', extensionName: 'developer' } } } },
    { rawInput: { path: '.' }, _meta: { goose: { toolCall: { toolName: 'developer__tree', extensionName: 'developer' } } } },
  ),
  delegate: gooseUpdate(
    { content: text('**Findings**\n\nThe entry points exist.'), _meta: { goose: { toolCall: { toolName: 'delegate', extensionName: 'summon' } } } },
    { rawInput: { instructions: 'Inspect the project', source: 'explore' }, _meta: { goose: { toolCall: { toolName: 'delegate', extensionName: 'summon' } } } },
  ),
  todo_write: gooseUpdate(
    { content: text('Updated'), _meta: { goose: { toolCall: { toolName: 'todo__todo_write', extensionName: 'todo' } } } },
    { rawInput: { content: '- [x] Completed task\n- [ ] Pending task' }, _meta: { goose: { toolCall: { toolName: 'todo__todo_write', extensionName: 'todo' } } } },
  ),
  recall: gooseUpdate(
    { content: text('The reader prefers tabs.'), _meta: { goose: { toolCall: { toolName: 'memory__recall', extensionName: 'memory' } } } },
    { rawInput: { query: 'preferences' }, _meta: { goose: { toolCall: { toolName: 'memory__recall', extensionName: 'memory' } } } },
  ),
}

/**
 * The sentence every failed fixture carries.
 *
 * Synthetic on purpose. Goose's own error wording is not confirmable from this
 * repository, and the guard asks about the LADDER -- the outcome word, the brand, the
 * kind and the request -- rather than about any provider's choice of words.
 */
const ERROR_TEXT = 'The tool reported an error.'

/**
 * The FAILED frame of the call one successful fixture already states.
 *
 * It keeps the frame's identity -- the call id and the `_meta` record that carries the tool name, which describe the TOOL and never
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
    payload: { sessionUpdate: 'tool_call_update', toolCallId: CALL, status: 'failed', content: text(ERROR_TEXT), _meta: fixture?.payload._meta },
    ...(fixture?.options !== undefined ? { options: fixture.options } : {}),
    kind,
    name,
    status,
  }
}

export const GOOSE_TOOL_RESULTS: ToolResultCheck = {
  provider: AgentProvider.GOOSE,
  fixtures: FIXTURES,
  failures: [
    failed('execute', 'shell'),
    failed('edit', 'edit'),
    failed('write', 'write'),
    failed('read', 'read'),
    failed('list', 'tree'),
    failed('agent', 'delegate'),
    failed('todo', 'todo_write'),
    failed('mcp', 'recall'),
  ],
  noFailure: {},
  noResult: {},
  unparsed: {
    edit: 'Goose states a completed edit in words rather than a diff; the requested change draws beside them.',
    read_image: 'The text summary beside the picture is a sentence, not the numbered file body the read body draws.',
    write: 'Goose states a completed write in words rather than a diff; the requested change draws beside them.',
    tree: 'A tree with branch art and line counts is not the flat entry list the list kind draws.',
  },
}
