import type { ToolKind } from '~/components/chat/model/toolKind'
import type { ToolFailureFixture, ToolResultCheck, ToolResultFixture } from '~/test-support/toolVocabulary'
import { OH_MY_PI_TOOL } from '~/generated/contracts/ohmypi-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'

/**
 * A successful omp `tool_execution_end` frame, paired with the start frame that states
 * its arguments. omp's end frame carries no arguments of its own.
 */
function end(toolName: string, result: Record<string, unknown>, args: Record<string, unknown>): ToolResultFixture {
  return {
    payload: { type: 'tool_execution_end', toolCallId: 'call_1', toolName, result, isError: false },
    options: {
      request: {
        wrapper: null,
        topLevel: null,
        parentObject: { type: 'tool_execution_start', toolCallId: 'call_1', toolName, args },
        rawText: '',
        supplementalContent: undefined,
        messageMetadata: undefined,
      },
    },
  }
}

const text = (value: string) => [{ type: 'text', text: value }]

/**
 * One successful frame for every tool the kind table holds.
 *
 * `read`, `write`, `edit`, `grep`, `glob` and `bash` are omp 18.2.11's own frames, from
 * a probe of the real CLI against a scripted model, with the absolute paths shortened.
 */
const FIXTURES: Readonly<Record<string, ToolResultFixture>> = {
  [OH_MY_PI_TOOL.Read]: end(OH_MY_PI_TOOL.Read, {
    content: text('[notes.txt#C789]\n1:alpha one\n2:beta two\n3:gamma three'),
    details: { totalLines: 3, displayContent: { text: 'alpha one\nbeta two\ngamma three', startLine: 1, lineNumbers: [1, 2, 3] }, fileSize: 31 },
  }, { path: 'notes.txt' }),
  [OH_MY_PI_TOOL.Write]: end(OH_MY_PI_TOOL.Write, {
    content: text('[new.txt#F089]\nSuccessfully wrote 11 bytes to new.txt'),
    details: { resolvedPath: '/p/new.txt' },
  }, { path: 'new.txt', content: 'fresh line\n' }),
  [OH_MY_PI_TOOL.Edit]: end(OH_MY_PI_TOOL.Edit, {
    content: text('[notes.txt#9C79]\n1:alpha one\n2:beta TWO\n3:gamma three'),
    details: { diff: ' 1|alpha one\n-2|beta two\n+2|beta TWO\n 3|gamma three', firstChangedLine: 2, op: 'update', path: '/p/notes.txt', oldText: 'alpha one\nbeta two\ngamma three\n', newText: 'alpha one\nbeta TWO\ngamma three\n' },
  }, { input: '[notes.txt#C789]\nPUT 2.=2:\n+beta TWO' }),
  [OH_MY_PI_TOOL.ApplyPatch]: end(OH_MY_PI_TOOL.ApplyPatch, {
    content: text('Done'),
    details: { diff: '', path: '/p/a.ts', oldText: 'old\n', newText: 'new\n' },
  }, { input: '*** Begin Patch\n*** Update File: a.ts\n@@\n-old\n+new\n*** End Patch' }),
  [OH_MY_PI_TOOL.Grep]: end(OH_MY_PI_TOOL.Grep, {
    content: text('# notes.txt#C789\n*1:alpha one\n 2:beta two\n 3:gamma three'),
    details: { scopePath: '.', matchCount: 1, fileCount: 1, files: ['notes.txt'], fileMatches: [{ path: 'notes.txt', count: 1 }], truncated: false },
  }, { pattern: 'alpha', path: '.' }),
  [OH_MY_PI_TOOL.Glob]: end(OH_MY_PI_TOOL.Glob, {
    content: text('new.txt\nnotes.txt'),
    details: { scopePath: '.', fileCount: 2, files: ['new.txt', 'notes.txt'], truncated: false },
  }, { path: '*.txt' }),
  [OH_MY_PI_TOOL.Bash]: end(OH_MY_PI_TOOL.Bash, {
    content: text('probe-output\n\n\nWall time: 0.05 seconds'),
    details: { timeoutSeconds: 300, wallTimeMs: 49.99 },
  }, { command: 'echo probe-output' }),
  [OH_MY_PI_TOOL.Eval]: end(OH_MY_PI_TOOL.Eval, {
    content: text('2'),
    details: { cells: [{ index: 0, code: '1 + 1', language: 'js', output: '2', status: 'complete', durationMs: 3, exitCode: 0 }] },
  }, { language: 'js', code: '1 + 1' }),
  [OH_MY_PI_TOOL.Find]: end(OH_MY_PI_TOOL.Find, { content: text('src/a.ts: the parser') }, { query: 'where is the parser' }),
  [OH_MY_PI_TOOL.AstGrep]: end(OH_MY_PI_TOOL.AstGrep, { content: text('src/a.ts:3: foo()') }, { pattern: 'foo($$$)' }),
  [OH_MY_PI_TOOL.Task]: end(OH_MY_PI_TOOL.Task, {
    content: text('<task-result id="ScoutOne" agent="task" status="completed">\n<output>\n"child says hello"\n</output>\n</task-result>'),
    details: { results: [{ index: 0, id: 'ScoutOne', agent: 'task', assignment: 'Say hello.', exitCode: 0, output: '"child says hello"', stderr: '', durationMs: 373, tokens: 2460 }] },
  }, { context: 'Probe.', tasks: [{ name: 'ScoutOne', agent: 'task', task: 'Say hello.' }] }),
  [OH_MY_PI_TOOL.Hub]: end(OH_MY_PI_TOOL.Hub, { content: text('Sent to ScoutOne.') }, { op: 'send', to: 'ScoutOne', message: 'Hurry up.' }),
  [OH_MY_PI_TOOL.Todo]: end(OH_MY_PI_TOOL.Todo, {
    content: text('Remaining items (2)'),
    details: { op: 'init', phases: [{ name: 'Build', tasks: [{ content: 'Write code', status: 'in_progress' }, { content: 'Test it', status: 'pending' }] }], storage: 'session' },
  }, { op: 'init', list: [{ phase: 'Build', items: ['Write code', 'Test it'] }] }),
  [OH_MY_PI_TOOL.WebSearch]: end(OH_MY_PI_TOOL.WebSearch, {
    content: text('The answer.'),
    details: { response: { provider: 'exa', answer: 'The answer.', sources: [{ title: 'Doc', url: 'https://example.com/doc' }] } },
  }, { query: 'leapmux' }),
  [OH_MY_PI_TOOL.Ask]: end(OH_MY_PI_TOOL.Ask, {
    content: text('User selected: SQLite'),
    details: { question: 'Which database?', options: ['SQLite', 'PostgreSQL'], multi: false, selectedOptions: ['SQLite'] },
  }, { questions: [{ id: 'db', question: 'Which database?', options: [{ label: 'SQLite' }, { label: 'PostgreSQL' }], recommended: 0 }] }),
  [OH_MY_PI_TOOL.Yield]: end(OH_MY_PI_TOOL.Yield, { content: text('Result submitted.'), details: { data: 'child says hello', status: 'success' } }, { data: 'child says hello' }),
  [OH_MY_PI_TOOL.Goal]: end(OH_MY_PI_TOOL.Goal, { content: text('Goal created.'), details: { op: 'create' } }, { op: 'create', objective: 'Ship it' }),
  [OH_MY_PI_TOOL.Think]: end(OH_MY_PI_TOOL.Think, { content: text('Noted.') }, { thought: 'Consider the edge cases.' }),
}

/**
 * The sentence every failed fixture carries. Synthetic on purpose: the guard asks about
 * the ladder -- the outcome word, the brand, the kind and the request -- and never about
 * omp's choice of words.
 */
const ERROR_TEXT = 'The tool reported an error.'

/** The FAILED end frame of the call one successful fixture already states. */
function failed(kind: ToolKind, name: string, status: ToolFailureFixture['status'] = 'failed'): ToolFailureFixture {
  const fixture = FIXTURES[name]
  return {
    payload: { type: 'tool_execution_end', toolCallId: 'call_1', toolName: name, result: { content: text(ERROR_TEXT), details: {} }, isError: true },
    ...(fixture?.options !== undefined ? { options: fixture.options } : {}),
    kind,
    name,
    status,
  }
}

export const OH_MY_PI_TOOL_RESULTS: ToolResultCheck = {
  provider: AgentProvider.OH_MY_PI,
  fixtures: FIXTURES,
  failures: [
    failed('read', OH_MY_PI_TOOL.Read),
    failed('write', OH_MY_PI_TOOL.Write),
    failed('edit', OH_MY_PI_TOOL.Edit),
    failed('grep', OH_MY_PI_TOOL.Grep),
    failed('glob', OH_MY_PI_TOOL.Glob),
    failed('search', OH_MY_PI_TOOL.Find),
    failed('execute', OH_MY_PI_TOOL.Bash),
    failed('agent', OH_MY_PI_TOOL.Task),
    failed('message', OH_MY_PI_TOOL.Hub),
    failed('todo', OH_MY_PI_TOOL.Todo),
    failed('web_search', OH_MY_PI_TOOL.WebSearch),
    failed('question', OH_MY_PI_TOOL.Ask),
    failed('report', OH_MY_PI_TOOL.Goal),
    failed('think', OH_MY_PI_TOOL.Think),
  ],
  noFailure: {},
  noResult: {},
  unparsed: {},
}
