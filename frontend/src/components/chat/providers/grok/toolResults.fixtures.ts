import type { ToolKind } from '~/components/chat/model/toolKind'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ToolFailureFixture, ToolResultCheck, ToolResultFixture } from '~/test-support/toolVocabulary'
import { GROK_TOOL } from '~/generated/contracts/grok-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../testUtils'
import { GROK_TOOL_NAME } from './toolKinds'

const CALL = 'call_2_0'

/**
 * One finished Grok call, and the first `tool_call` that opened it.
 *
 * The opening frame states the name as its title, the model's own arguments, no
 * kind, and Grok's identity in `_meta["x.ai/tool"]`. The finished frame is the one
 * the worker stores: the presentation fields -- the kind, a prose title and the
 * normalized arguments with their variant tag -- merged with the final status, the
 * content and the record Grok writes into `rawOutput`.
 */
function grokUpdate(name: string, args: Record<string, unknown>, frame: Record<string, unknown>): ToolResultFixture {
  return {
    payload: { sessionUpdate: 'tool_call_update', toolCallId: CALL, status: 'completed', ...frame },
    options: {
      spanType: 'tool_call_update',
      request: input({ sessionUpdate: 'tool_call', toolCallId: CALL, title: name, rawInput: args, _meta: { 'x.ai/tool': { version: 1, name } } }, null, AgentProvider.GROK_BUILD) as ParsedMessageContent,
    },
  }
}

function text(value: string) {
  return [{ type: 'content', content: { type: 'text', text: value } }]
}

function bytes(value: string): number[] {
  return [...new TextEncoder().encode(value)]
}

const FIXTURES: Readonly<Record<string, ToolResultFixture>> = {
  [GROK_TOOL_NAME.RunTerminalCommand]: grokUpdate(GROK_TOOL_NAME.RunTerminalCommand, { command: 'printf hello', description: 'Say hello' }, {
    kind: 'execute',
    title: 'Execute `printf hello`',
    rawInput: { variant: 'Bash', command: 'printf hello', description: 'Say hello', is_background: false },
    content: text('hello'),
    rawOutput: { type: 'Bash', output: bytes('hello'), output_for_prompt: 'exit: 0\nhello', exit_code: 0, command: 'printf hello', truncated: false, signal: null, timed_out: false },
  }),
  [GROK_TOOL_NAME.Monitor]: grokUpdate(GROK_TOOL_NAME.Monitor, { command: 'tail -f app.log', description: 'Watch the log' }, {
    kind: 'execute',
    content: text('Monitoring started.'),
  }),
  [GROK_TOOL_NAME.ReadFile]: grokUpdate(GROK_TOOL_NAME.ReadFile, { target_file: '/p/a.ts' }, {
    kind: 'read',
    title: 'Read `/p/a.ts`',
    rawInput: { variant: 'ReadFile', target_file: '/p/a.ts' },
    locations: [{ path: '/p/a.ts' }],
    content: text('1→one\n2→two\n'),
    rawOutput: { type: 'ReadFile', FileContent: { content: '1→one\n2→two\n', absolute_path: '/p/a.ts', offset: null, raw_output: 'one\ntwo\n', total_lines: 3 } },
  }),
  [GROK_TOOL_NAME.SearchReplace]: grokUpdate(GROK_TOOL_NAME.SearchReplace, { file_path: '/p/a.ts', old_string: 'before', new_string: 'after' }, {
    kind: 'edit',
    title: 'Edit `/p/a.ts`',
    rawInput: { variant: 'SearchReplace', file_path: '/p/a.ts', old_string: 'before', new_string: 'after', replace_all: false },
    content: [{ type: 'diff', path: '/p/a.ts', oldText: 'before', newText: 'after', _meta: { old_line: 1, new_line: 1 } }],
    locations: [{ path: '/p/a.ts' }],
    rawOutput: { type: 'SearchReplace', EditsApplied: { old_string: 'before', new_string: 'after', absolute_path: '/p/a.ts', tool_output_for_prompt: 'The file /p/a.ts has been updated successfully.' } },
  }),
  [GROK_TOOL_NAME.Write]: grokUpdate(GROK_TOOL_NAME.Write, { file_path: '/p/n.ts', content: 'written()\n' }, {
    kind: 'edit',
    title: 'Write `/p/n.ts`',
    rawInput: { variant: 'Write', file_path: '/p/n.ts', content: 'written()\n' },
    content: [{ type: 'diff', path: '/p/n.ts', oldText: '', newText: 'written()\n' }],
    locations: [{ path: '/p/n.ts' }],
    rawOutput: { type: 'SearchReplace', EditsApplied: { old_string: '', new_string: 'written()\n', absolute_path: '/p/n.ts', tool_output_for_prompt: 'The file /p/n.ts has been created.' } },
  }),
  [GROK_TOOL_NAME.ListDir]: grokUpdate(GROK_TOOL_NAME.ListDir, { target_directory: '/p' }, {
    kind: 'other',
    title: 'List `/p`',
    rawInput: { variant: 'ListDir', target_directory: '/p' },
    rawOutput: { type: 'ListDir', Content: { content: '- /p/\n  - a.ts\n  - src/\n    - b.ts', absolute_root_path: '/p' } },
  }),
  [GROK_TOOL_NAME.Grep]: grokUpdate(GROK_TOOL_NAME.Grep, { pattern: 'needle', path: '/p' }, {
    kind: 'search',
    title: 'needle',
    rawInput: { 'variant': 'Grep', 'pattern': 'needle', 'path': '/p', 'glob': null, '-i': false, 'type': null, 'multiline': false },
    content: text('found 1 matches'),
    rawOutput: { type: 'GrepSearch', stdout: bytes('Found 1 matching lines\n/p/a.ts\n3:needle\n'), stderr: [], exit_code: 0, match_count: 1, file_matches: [{ path: '/p/a.ts', matches: [{ line_number: 3, content: 'needle' }] }] },
  }),
  [GROK_TOOL_NAME.GetOutput]: grokUpdate(GROK_TOOL_NAME.GetOutput, { task_ids: ['task-1'], timeout_ms: 1000 }, {
    content: text('Task task-1 completed.\nhello'),
  }),
  [GROK_TOOL_NAME.Kill]: grokUpdate(GROK_TOOL_NAME.Kill, { task_id: 'task-1' }, {
    content: text('Task task-1 was killed.'),
  }),
  [GROK_TOOL_NAME.SchedulerCreate]: grokUpdate(GROK_TOOL_NAME.SchedulerCreate, { interval: '5m', prompt: 'Check the build' }, {
    content: text('Scheduled task sched-1 every 5m.'),
  }),
  [GROK_TOOL_NAME.SchedulerDelete]: grokUpdate(GROK_TOOL_NAME.SchedulerDelete, { id: 'sched-1' }, {
    content: text('Deleted scheduled task sched-1.'),
  }),
  [GROK_TOOL_NAME.SchedulerList]: grokUpdate(GROK_TOOL_NAME.SchedulerList, {}, {
    content: text('No scheduled tasks.'),
  }),
  [GROK_TOOL_NAME.SearchTool]: grokUpdate(GROK_TOOL_NAME.SearchTool, { query: 'issues' }, {
    content: text('linear__list_issues: List the issues of a team.'),
    rawOutput: { type: 'SearchTool', result_count: 1, content: 'linear__list_issues: List the issues of a team.' },
  }),
  [GROK_TOOL_NAME.WebFetch]: grokUpdate(GROK_TOOL_NAME.WebFetch, { url: 'https://example.com' }, {
    kind: 'fetch',
    content: text('# Example\n\n**Fetched body**'),
  }),
  [GROK_TOOL_NAME.WebSearch]: grokUpdate(GROK_TOOL_NAME.WebSearch, { query: 'leapmux' }, {
    kind: 'search',
    content: text('1. LeapMux -- https://example.com'),
  }),
  [GROK_TOOL_NAME.AskUserQuestion]: grokUpdate(GROK_TOOL_NAME.AskUserQuestion, { questions: [{ question: 'Which database?', options: [{ label: 'Postgres', description: 'Relational' }], multi_select: false }] }, {
    kind: 'other',
    title: 'Ask 1 question',
    rawInput: { variant: 'AskUserQuestion', questions: [{ question: 'Which database?', options: [{ label: 'Postgres', description: 'Relational' }], multiSelect: null }] },
    content: text('User has answered your questions: "Which database?"="Postgres". You can now continue with the user\'s answers in mind.'),
    rawOutput: { type: 'AskUserQuestion', UserAnswered: { message: 'User has answered your questions: "Which database?"="Postgres". You can now continue with the user\'s answers in mind.' } },
  }),
  [GROK_TOOL_NAME.EnterPlanMode]: grokUpdate(GROK_TOOL_NAME.EnterPlanMode, {}, {
    kind: 'other',
    title: 'Plan mode entered',
    rawInput: { variant: 'EnterPlanMode' },
    content: text('You have entered plan mode.'),
  }),
  [GROK_TOOL_NAME.ExitPlanMode]: grokUpdate(GROK_TOOL_NAME.ExitPlanMode, {}, {
    kind: 'other',
    title: 'Plan mode exited',
    rawInput: { variant: 'ExitPlanMode' },
    content: text('Plan file: /home/.grok/sessions/s/plan.md'),
    rawOutput: { type: 'ExitPlanMode', EmptyPlan: { message: 'Plan mode exit approved.', plan_file_path: '/home/.grok/sessions/s/plan.md' } },
  }),
  [GROK_TOOL.SpawnSubagent]: grokUpdate(GROK_TOOL.SpawnSubagent, { prompt: 'List the files', description: 'List files', background: false }, {
    kind: 'other',
    title: 'List files',
    rawInput: { variant: 'Task', prompt: 'List the files', description: 'List files', run_in_background: false, task_id: null },
    content: text('Done.\n\n<subagent_meta>id=sub-1, tool_calls=1, turns=1, duration_ms=558</subagent_meta>'),
    rawOutput: { type: 'SubagentCompleted', output: 'Done.', subagent_id: 'sub-1', subagent_type: 'general-purpose', tool_calls: 1, turns: 1, duration_ms: 558, worktree_path: null, resume_from_hint: 'sub-1' },
  }),
  [GROK_TOOL_NAME.Workflow]: grokUpdate(GROK_TOOL_NAME.Workflow, { source: 'export default async () => {}' }, {
    content: text('Workflow review-changes started.'),
    rawOutput: { type: 'Workflow', run_id: 'wf-1', task_id: 'wf-1', name: 'review-changes', message: 'Workflow review-changes started.' },
  }),
  [GROK_TOOL_NAME.TodoWrite]: grokUpdate(GROK_TOOL_NAME.TodoWrite, { todos: [{ id: '2', content: 'Write hello file', status: 'in_progress' }], merge: true }, {
    kind: 'think',
    title: 'Updating plan',
    rawInput: { variant: 'TodoWrite', merge: true, todos: [{ id: '2', content: 'Write hello file', status: 'in_progress' }] },
    rawOutput: { type: 'Todo', TodosUpdated: { todos: [{ content: 'Write probe file', status: 'completed' }, { content: 'Write hello file', status: 'in_progress' }] } },
  }),
  [GROK_TOOL_NAME.UseTool]: grokUpdate(GROK_TOOL_NAME.UseTool, { tool_name: 'linear__list_issues', tool_input: { team: 'core' } }, {
    content: text('3 issues'),
  }),
}

/** The sentence every failed fixture carries. Synthetic on purpose: the guard is about the ladder, not the wording. */
const ERROR_TEXT = 'The tool reported an error.'

/** The FAILED frame of the call one successful fixture states, with the same opening frame. */
function failed(kind: ToolKind, name: string, status: ToolFailureFixture['status'] = 'failed'): ToolFailureFixture {
  const fixture = FIXTURES[name]
  return {
    payload: { sessionUpdate: 'tool_call_update', toolCallId: CALL, status: 'failed', content: text(ERROR_TEXT) },
    ...(fixture?.options !== undefined ? { options: fixture.options } : {}),
    kind,
    name,
    status,
  }
}

export const GROK_TOOL_RESULTS: ToolResultCheck = {
  provider: AgentProvider.GROK_BUILD,
  fixtures: FIXTURES,
  failures: [
    failed('execute', GROK_TOOL_NAME.RunTerminalCommand),
    failed('read', GROK_TOOL_NAME.ReadFile),
    failed('edit', GROK_TOOL_NAME.SearchReplace),
    failed('write', GROK_TOOL_NAME.Write),
    failed('list', GROK_TOOL_NAME.ListDir),
    failed('grep', GROK_TOOL_NAME.Grep),
    failed('task', GROK_TOOL_NAME.Kill),
    failed('trigger', GROK_TOOL_NAME.SchedulerCreate),
    failed('search', GROK_TOOL_NAME.SearchTool),
    failed('fetch', GROK_TOOL_NAME.WebFetch),
    failed('web_search', GROK_TOOL_NAME.WebSearch),
    failed('question', GROK_TOOL_NAME.AskUserQuestion),
    failed('switch_mode', GROK_TOOL_NAME.ExitPlanMode),
    failed('agent', GROK_TOOL.SpawnSubagent),
    failed('todo', GROK_TOOL_NAME.TodoWrite),
    failed('mcp', GROK_TOOL_NAME.UseTool),
  ],
  noFailure: {},
  noResult: {},
  unparsed: {},
}
