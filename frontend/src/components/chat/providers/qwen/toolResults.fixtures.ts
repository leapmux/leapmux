import type { ToolKind } from '~/components/chat/model/toolKind'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ToolFailureFixture, ToolResultCheck, ToolResultFixture } from '~/test-support/toolVocabulary'
import { QWEN_TOOL } from '~/generated/contracts/qwen-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../testUtils'
import { QWEN_TOOL_NAME } from './toolKinds'

const CALL = 'call_e4a50b50ee'

/**
 * One finished Qwen call, and the frame that opened it.
 *
 * Qwen opens a call with its display title, the ACP kind and the real tool name in
 * `_meta.toolName`, and it states the arguments on the opening frame. The finished
 * frame states the content Qwen gives the model and a `rawOutput` record for the
 * tools that write one; it repeats `_meta.toolName`.
 */
function qwenUpdate(name: string, kind: string, title: string, args: Record<string, unknown>, frame: Record<string, unknown>): ToolResultFixture {
  return {
    payload: { sessionUpdate: 'tool_call_update', toolCallId: CALL, status: 'completed', _meta: { toolName: name, provenance: 'builtin' }, ...frame },
    options: {
      spanType: 'tool_call_update',
      request: input({ sessionUpdate: 'tool_call', toolCallId: CALL, status: 'in_progress', title, kind, rawInput: args, locations: [], _meta: { toolName: name, provenance: 'builtin' } }, null, AgentProvider.QWEN_CODE) as ParsedMessageContent,
    },
  }
}

function text(value: string) {
  return [{ type: 'content', content: { type: 'text', text: value } }]
}

const FIXTURES: Readonly<Record<string, ToolResultFixture>> = {
  [QWEN_TOOL.RunShellCommand]: qwenUpdate(QWEN_TOOL.RunShellCommand, 'execute', 'Shell: printf hello (Say hello)', { command: 'printf hello', description: 'Say hello' }, {
    content: text('Command: printf hello\nDirectory: (root)\nOutput: hello\nError: (none)\nExit Code: 0\nSignal: (none)\nProcess Group PGID: 7'),
    rawOutput: { type: 'shell_result', version: 1, directory: '/p', exitCode: 0, signal: null, pid: 7, outcome: 'completed', output: 'hello', text: 'hello', error: null, truncated: false },
  }),
  [QWEN_TOOL_NAME.Monitor]: qwenUpdate(QWEN_TOOL_NAME.Monitor, 'execute', 'Monitor: tail -f app.log', { command: 'tail -f app.log', description: 'Watch the log' }, {
    content: text('Monitor started.'),
    rawOutput: 'Monitor started.',
  }),
  [QWEN_TOOL_NAME.ReadFile]: qwenUpdate(QWEN_TOOL_NAME.ReadFile, 'read', 'ReadFile: a.ts', { file_path: '/p/a.ts' }, {
    content: text('one\ntwo\n'),
    rawOutput: 'one\ntwo\n',
  }),
  [QWEN_TOOL_NAME.ZoomImage]: qwenUpdate(QWEN_TOOL_NAME.ZoomImage, 'read', 'ZoomImage: shot.png', { file_path: '/p/shot.png', x1: 0, y1: 0, x2: 10, y2: 10 }, {
    content: [{ type: 'content', content: { type: 'text', text: 'The region of /p/shot.png.' } }, { type: 'content', content: { type: 'image', data: 'aGk=', mimeType: 'image/png' } }],
  }),
  [QWEN_TOOL_NAME.Edit]: qwenUpdate(QWEN_TOOL_NAME.Edit, 'edit', 'Edit: a.ts', { file_path: '/p/a.ts', old_string: 'before', new_string: 'after' }, {
    content: [{ type: 'diff', path: '/p/a.ts', oldText: 'const a = before\n', newText: 'const a = after\n' }],
  }),
  [QWEN_TOOL_NAME.NotebookEdit]: qwenUpdate(QWEN_TOOL_NAME.NotebookEdit, 'edit', 'NotebookEdit: a.ipynb', { notebook_path: '/p/a.ipynb', new_source: 'x = 1' }, {
    content: [{ type: 'diff', path: '/p/a.ipynb', oldText: '{"cells":[]}', newText: '{"cells":[{"source":"x = 1"}]}' }],
  }),
  [QWEN_TOOL_NAME.WriteFile]: qwenUpdate(QWEN_TOOL_NAME.WriteFile, 'edit', 'WriteFile: n.ts', { file_path: '/p/n.ts', content: 'written()\n' }, {
    content: [{ type: 'diff', path: '/p/n.ts', oldText: '', newText: 'written()\n' }],
  }),
  [QWEN_TOOL_NAME.GrepSearch]: qwenUpdate(QWEN_TOOL_NAME.GrepSearch, 'search', 'Grep: needle', { pattern: 'needle', path: '/p' }, {
    content: text('Found 1 match for pattern "needle" in path "/p":\n---\nFile: a.ts\nL3: needle\n---\n'),
  }),
  [QWEN_TOOL_NAME.Glob]: qwenUpdate(QWEN_TOOL_NAME.Glob, 'search', 'FindFiles: *.ts', { pattern: '*.ts' }, {
    content: text('Found 1 file(s) matching "*.ts" within /p, sorted by modification time (newest first):\n---\n/p/a.ts'),
  }),
  [QWEN_TOOL_NAME.ListDirectory]: qwenUpdate(QWEN_TOOL_NAME.ListDirectory, 'search', 'ReadFolder: /p', { path: '/p' }, {
    content: text('Listed 2 item(s) in /p:\n---\n[DIR] src\na.ts'),
  }),
  [QWEN_TOOL_NAME.Lsp]: qwenUpdate(QWEN_TOOL_NAME.Lsp, 'search', 'Lsp: definition', { operation: 'definition', query: 'main' }, {
    content: text('main is defined at /p/a.ts:3'),
  }),
  [QWEN_TOOL_NAME.ToolSearch]: qwenUpdate(QWEN_TOOL_NAME.ToolSearch, 'search', 'ToolSearch: cron', { query: 'cron' }, {
    content: text('cron_create: Schedule a prompt.'),
  }),
  [QWEN_TOOL_NAME.WebFetch]: qwenUpdate(QWEN_TOOL_NAME.WebFetch, 'fetch', 'WebFetch: https://example.com', { url: 'https://example.com', prompt: 'Summarize' }, {
    content: text('# Example\n\n**Fetched body**'),
  }),
  [QWEN_TOOL_NAME.WebSearch]: qwenUpdate(QWEN_TOOL_NAME.WebSearch, 'search', 'WebSearch: leapmux', { query: 'leapmux' }, {
    content: text('1. LeapMux -- https://example.com'),
  }),
  [QWEN_TOOL.AskUserQuestion]: qwenUpdate(QWEN_TOOL.AskUserQuestion, 'think', 'AskUserQuestion: Ask user 1 question', { questions: [{ question: 'Which color do you want?', header: 'Color', options: [{ label: 'Red', description: 'r' }, { label: 'Blue', description: 'b' }], multiSelect: false }] }, {
    content: text('User has provided the following answers:\n\n**Color**: Blue'),
    rawOutput: { type: 'ask_user_question_answers', text: 'User has provided the following answers:\n\n**Color**: Blue', answers: [{ question: 'Which color do you want?', answer: 'Blue' }] },
  }),
  [QWEN_TOOL.ExitPlanMode]: qwenUpdate(QWEN_TOOL.ExitPlanMode, 'switch_mode', 'ExitPlanMode: Plan:', { plan: '1. Do X' }, {
    content: text('User approved. You can now start coding.'),
    rawOutput: { type: 'plan_summary', message: 'User approved.', plan: '1. Do X' },
  }),
  [QWEN_TOOL_NAME.EnterPlanMode]: qwenUpdate(QWEN_TOOL_NAME.EnterPlanMode, 'switch_mode', 'EnterPlanMode', {}, {
    content: text('Entered plan mode.'),
  }),
  [QWEN_TOOL_NAME.EnterWorktree]: qwenUpdate(QWEN_TOOL_NAME.EnterWorktree, 'other', 'EnterWorktree: feature', { name: 'feature' }, {
    content: text('Now working in the worktree feature.'),
  }),
  [QWEN_TOOL_NAME.ExitWorktree]: qwenUpdate(QWEN_TOOL_NAME.ExitWorktree, 'other', 'ExitWorktree: feature', { name: 'feature', action: 'keep' }, {
    content: text('Left the worktree feature.'),
  }),
  [QWEN_TOOL.Agent]: qwenUpdate(QWEN_TOOL.Agent, 'other', 'Agent: Child probe', { description: 'Child probe', prompt: 'List the files', subagent_type: 'general-purpose', run_in_background: false }, {
    content: text('Child done: listed.'),
    rawOutput: { type: 'task_execution', subagentName: 'general-purpose', taskDescription: 'Child probe', taskPrompt: 'List the files', executionMode: 'foreground', status: 'completed', terminateReason: 'GOAL', result: 'Child done: listed.', executionSummary: { rounds: 2, totalDurationMs: 205, totalToolCalls: 1, totalTokens: 220 } },
  }),
  [QWEN_TOOL.Workflow]: qwenUpdate(QWEN_TOOL.Workflow, 'other', 'Workflow', { script: 'await agent("x")' }, {
    content: text('Child done: listed.\n--- workflow run ---\nrunId: wf_1'),
    rawOutput: '```json\n{\n  "runId": "wf_1",\n  "phases": ["probe"],\n  "logs": [],\n  "result": "Child done: listed.",\n  "tokens": {"spent": 20, "total": null}\n}\n```',
  }),
  [QWEN_TOOL.TodoWrite]: qwenUpdate(QWEN_TOOL.TodoWrite, 'think', 'TodoWrite', { todos: [{ id: '1', content: 'Inspect code', status: 'in_progress' }] }, {
    content: text('Todos have been modified successfully.'),
  }),
  [QWEN_TOOL_NAME.CronCreate]: qwenUpdate(QWEN_TOOL_NAME.CronCreate, 'other', 'CronCreate', { cron: '*/5 * * * *', prompt: 'Check the build', recurring: true }, {
    content: text('Scheduled job cron-1.'),
    rawOutput: 'Scheduled job cron-1.',
  }),
  [QWEN_TOOL_NAME.CronDelete]: qwenUpdate(QWEN_TOOL_NAME.CronDelete, 'other', 'CronDelete', { id: 'cron-1' }, {
    content: text('Deleted job cron-1.'),
  }),
  [QWEN_TOOL_NAME.CronList]: qwenUpdate(QWEN_TOOL_NAME.CronList, 'other', 'CronList', {}, {
    content: text('No active cron jobs or loop wakeups.'),
    rawOutput: 'No active cron jobs or loop wakeups.',
  }),
  [QWEN_TOOL_NAME.LoopWakeup]: qwenUpdate(QWEN_TOOL_NAME.LoopWakeup, 'other', 'LoopWakeup', { delaySeconds: 60, prompt: 'Look again', reason: 'Wait for CI' }, {
    content: text('The next turn starts in 60 seconds.'),
  }),
  [QWEN_TOOL_NAME.ImageGen]: qwenUpdate(QWEN_TOOL_NAME.ImageGen, 'other', 'ImageGen', { prompt: 'A red dot' }, {
    content: [{ type: 'content', content: { type: 'image', data: 'aGk=', mimeType: 'image/png' } }],
  }),
  [QWEN_TOOL_NAME.ListAgents]: qwenUpdate(QWEN_TOOL_NAME.ListAgents, 'other', 'ListAgents', {}, {
    content: text('general-purpose: A general agent.'),
  }),
  [QWEN_TOOL_NAME.ReportFindings]: qwenUpdate(QWEN_TOOL_NAME.ReportFindings, 'other', 'ReportFindings', { severity: 'high', file: '/p/a.ts', summary: 'A bug', failureScenario: 'It fails' }, {
    content: text('Finding recorded.'),
  }),
  [QWEN_TOOL_NAME.SendMessage]: qwenUpdate(QWEN_TOOL_NAME.SendMessage, 'other', 'SendMessage', { message: 'Continue', task_id: 'general-purpose-call_1' }, {
    content: text('Message delivered.'),
  }),
  [QWEN_TOOL_NAME.Skill]: qwenUpdate(QWEN_TOOL_NAME.Skill, 'other', 'Skill: review', { skill: 'review' }, {
    content: text('Loaded the review skill.'),
  }),
  [QWEN_TOOL_NAME.TaskStop]: qwenUpdate(QWEN_TOOL_NAME.TaskStop, 'other', 'TaskStop', { task_id: 'general-purpose-call_1' }, {
    content: text('Stopped the task.'),
  }),
}

/** The sentence every failed fixture carries. Synthetic on purpose: the guard is about the ladder, not the wording. */
const ERROR_TEXT = 'The tool reported an error.'

/** The FAILED frame of the call one successful fixture states, with the same opening frame. */
function failed(kind: ToolKind, name: string, status: ToolFailureFixture['status'] = 'failed'): ToolFailureFixture {
  const fixture = FIXTURES[name]
  return {
    payload: { sessionUpdate: 'tool_call_update', toolCallId: CALL, status: 'failed', content: text(ERROR_TEXT), _meta: { toolName: name } },
    ...(fixture?.options !== undefined ? { options: fixture.options } : {}),
    kind,
    name,
    status,
  }
}

export const QWEN_TOOL_RESULTS: ToolResultCheck = {
  provider: AgentProvider.QWEN_CODE,
  fixtures: FIXTURES,
  failures: [
    failed('execute', QWEN_TOOL.RunShellCommand),
    failed('read', QWEN_TOOL_NAME.ReadFile),
    failed('edit', QWEN_TOOL_NAME.Edit),
    failed('write', QWEN_TOOL_NAME.WriteFile),
    failed('grep', QWEN_TOOL_NAME.GrepSearch),
    failed('glob', QWEN_TOOL_NAME.Glob),
    failed('list', QWEN_TOOL_NAME.ListDirectory),
    failed('search', QWEN_TOOL_NAME.ToolSearch),
    failed('fetch', QWEN_TOOL_NAME.WebFetch),
    failed('web_search', QWEN_TOOL_NAME.WebSearch),
    failed('question', QWEN_TOOL.AskUserQuestion),
    failed('switch_mode', QWEN_TOOL.ExitPlanMode),
    failed('agent', QWEN_TOOL.Agent),
    failed('todo', QWEN_TOOL.TodoWrite),
    failed('trigger', QWEN_TOOL_NAME.CronCreate),
    failed('image', QWEN_TOOL_NAME.ImageGen),
    failed('agents', QWEN_TOOL_NAME.ListAgents),
    failed('report', QWEN_TOOL_NAME.ReportFindings),
    failed('message', QWEN_TOOL_NAME.SendMessage),
    failed('skill', QWEN_TOOL_NAME.Skill),
    failed('task', QWEN_TOOL_NAME.TaskStop),
  ],
  noFailure: {},
  noResult: {},
  unparsed: {},
}
