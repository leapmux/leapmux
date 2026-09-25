import type { ToolKind } from '~/components/chat/model/toolKind'
import type { ToolFailureFixture, ToolResultCheck, ToolResultFixture } from '~/test-support/toolVocabulary'
import { MIMO_TOOL, MIMO_TOOL_STATUS } from '~/generated/contracts/mimo-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { openingFrame, parsedFrame, toolFrame } from '~/test-support/mimoFixtures'

/** The input each fixture's call carries, which its failed frame states again. */
const INPUTS = new Map<string, Record<string, unknown>>()

/**
 * A finished call: its opening frame beside its final frame.
 *
 * The frames follow what MiMo 0.1.14 sent in the probes of `.tmp/probe/mimo-code/`:
 * the inputs spell MiMo's own snake-case keys, and each output is the text the tool
 * wrote for the model, with the metadata beside it.
 */
function done(tool: string, input: Record<string, unknown>, output: string, metadata: Record<string, unknown> = {}, title = ''): ToolResultFixture {
  INPUTS.set(tool, input)
  return {
    payload: toolFrame(tool, { status: MIMO_TOOL_STATUS.Completed, input, output, metadata, ...(title ? { title } : {}) }),
    options: { spanType: tool, request: parsedFrame(openingFrame(tool, input)) },
  }
}

const READ_OUTPUT = '<path>/p/a.ts</path>\n<type>file</type>\n<content>\n1: alpha\n2: beta\n\n(End of file - total 2 lines)\n</content>'
const GREP_OUTPUT = 'Found 2 matches\n/p/a.ts:\n  Line 1: alpha\n  Line 2: alphabet'
const SPAWN_OUTPUT = 'Background sub-session started. actor_id: general-1\nThe result will be delivered as a notification when complete.'
const EDIT_DIFF = 'Index: /p/a.ts\n===================================================================\n--- /p/a.ts\n+++ /p/a.ts\n@@ -1,1 +1,1 @@\n-alpha\n+omega\n'

const FIXTURES: Readonly<Record<string, ToolResultFixture>> = {
  [MIMO_TOOL.Bash]: done(MIMO_TOOL.Bash, { command: 'echo probe', description: 'Print probe output' }, 'probe\n', { output: 'probe\n', exit: 0, description: 'Print probe output', truncated: false }),
  [MIMO_TOOL.Read]: done(MIMO_TOOL.Read, { file_path: '/p/a.ts' }, READ_OUTPUT, { preview: 'alpha\nbeta', truncated: false, loaded: [] }),
  [MIMO_TOOL.Glob]: done(MIMO_TOOL.Glob, { pattern: '*.ts' }, '/p/a.ts\n/p/b.ts', { count: 2, truncated: false }),
  [MIMO_TOOL.Grep]: done(MIMO_TOOL.Grep, { pattern: 'alpha' }, GREP_OUTPUT, { matches: 2, truncated: false }),
  [MIMO_TOOL.Edit]: done(MIMO_TOOL.Edit, { file_path: '/p/a.ts', old_string: 'alpha', new_string: 'omega' }, 'Edit applied successfully.', { diff: EDIT_DIFF, filediff: { file: '/p/a.ts', patch: EDIT_DIFF, additions: 1, deletions: 1 } }),
  [MIMO_TOOL.MultiEdit]: done(MIMO_TOOL.MultiEdit, { file_path: '/p/a.ts', edits: [{ old_string: 'alpha', new_string: 'omega' }] }, 'Edit applied successfully.', { results: [{ diff: EDIT_DIFF }] }),
  [MIMO_TOOL.Write]: done(MIMO_TOOL.Write, { file_path: '/p/new.ts', content: 'fresh\n' }, 'Wrote file successfully.', { filepath: '/p/new.ts', exists: false }),
  [MIMO_TOOL.NotebookEdit]: done(MIMO_TOOL.NotebookEdit, { notebook_path: '/p/n.ipynb', cell_id: 'c1', new_source: 'print(1)', edit_mode: 'replace' }, 'Notebook updated: replace on n.ipynb.', { edit_mode: 'replace', cell_id: 'c1' }),
  [MIMO_TOOL.ApplyPatch]: done(MIMO_TOOL.ApplyPatch, { patch_text: '*** Begin Patch\n*** Update File: /p/a.ts\n@@\n-alpha\n+omega\n*** End Patch' }, 'Success. Updated the following files:\nM /p/a.ts', { diff: EDIT_DIFF, files: [{ filePath: '/p/a.ts', type: 'update', patch: EDIT_DIFF }] }),
  [MIMO_TOOL.ViewImage]: {
    payload: toolFrame(MIMO_TOOL.ViewImage, {
      status: MIMO_TOOL_STATUS.Completed,
      input: { path: '/p/dot.png' },
      output: 'Image read successfully',
      attachments: [{ type: 'file', mime: 'image/png', url: 'data:image/png;base64,iVBORw0KGgo=' }],
    }),
    options: { spanType: MIMO_TOOL.ViewImage, request: parsedFrame(openingFrame(MIMO_TOOL.ViewImage, { path: '/p/dot.png' })) },
  },
  [MIMO_TOOL.Actor]: done(MIMO_TOOL.Actor, { operation: { action: 'spawn', subagent_type: 'general', description: 'Probe helper', prompt: 'Run one command.' } }, SPAWN_OUTPUT, { sessionId: 'ses_test', actorId: 'general-1', model: { providerID: 'mock', modelID: 'alpha' } }, 'Probe helper'),
  [MIMO_TOOL.Task]: done(MIMO_TOOL.Task, { operation: { action: 'create', summary: 'Probe task' } }, 'Created T1 (open): Probe task', { id: 'T1', status: 'open', truncated: false }, 'Task created: T1'),
  [MIMO_TOOL.WebFetch]: done(MIMO_TOOL.WebFetch, { url: 'https://example.com', format: 'markdown' }, '# Example Domain', {}),
  [MIMO_TOOL.WebSearch]: done(MIMO_TOOL.WebSearch, { query: 'mimo code' }, 'Title: MiMo\nURL: https://example.com', {}),
  [MIMO_TOOL.CodeSearch]: done(MIMO_TOOL.CodeSearch, { query: 'react hooks' }, 'const [a, setA] = useState(0)', {}),
  [MIMO_TOOL.SkillSearch]: done(MIMO_TOOL.SkillSearch, { query: 'deploy' }, 'deploy: ship the build', {}),
  [MIMO_TOOL.Skill]: done(MIMO_TOOL.Skill, { name: 'deploy' }, '# Deploy\nRun the deploy.', {}),
  [MIMO_TOOL.PlanExit]: done(MIMO_TOOL.PlanExit, {}, 'User approved switching to build agent. Wait for further instructions.', { switched: true, feedback: '' }, 'Switching to build agent'),
  [MIMO_TOOL.Memory]: done(MIMO_TOOL.Memory, { operation: 'search', query: 'deploy' }, 'No memories found.', { count: 0 }),
  [MIMO_TOOL.History]: done(MIMO_TOOL.History, { operation: 'search', query: 'deploy' }, 'No matches.', { count: 0, truncated: false }),
  [MIMO_TOOL.Cron]: done(MIMO_TOOL.Cron, { operation: { action: 'schedule', cron: '0 9 * * 1', prompt: 'Weekly report' } }, 'Scheduled job j1.', { id: 'j1', kind: 'cron' }, 'Scheduled j1'),
  [MIMO_TOOL.Workflow]: done(MIMO_TOOL.Workflow, { operation: 'status', run_id: 'wf_1' }, 'Run wf_1 is running.', { runID: 'wf_1', status: 'running' }, 'Workflow wf_1'),
  [MIMO_TOOL.Session]: done(MIMO_TOOL.Session, { action: 'ask', session_id: 'ses_other', question: 'Status?' }, 'All green.', {}),
  [MIMO_TOOL.Exec]: done(MIMO_TOOL.Exec, { code: 'return 1 + 1' }, '2', {}),
  [MIMO_TOOL.Lsp]: done(MIMO_TOOL.Lsp, { operation: 'hover', file_path: '/p/a.ts', line: 1, character: 1 }, 'const alpha: number', { result: [] }),
  [MIMO_TOOL.McpToolSearch]: done(MIMO_TOOL.McpToolSearch, { query: 'github' }, 'github_create_issue', {}),
  [MIMO_TOOL.Question]: done(MIMO_TOOL.Question, { questions: [{ question: 'Which database?', header: 'Database', options: [{ label: 'SQLite' }, { label: 'Postgres' }] }] }, 'User has answered your questions: "Which database?"="SQLite".', { answers: [['SQLite']] }),
  [MIMO_TOOL.Invalid]: done(MIMO_TOOL.Invalid, { tool: 'reed', error: 'Unknown tool' }, 'The arguments provided to the tool are invalid.', {}),
}

/**
 * The sentence every failed fixture carries.
 *
 * Synthetic on purpose: the guard asks about the ladder -- the outcome word, the brand,
 * the kind and the request -- and never about the wording a provider chooses.
 */
const ERROR_TEXT = 'The tool reported an error.'

/** The FAILED frame of the call one successful fixture already states. */
function failed(kind: ToolKind, name: string, status: ToolFailureFixture['status'] = 'failed', error = ERROR_TEXT): ToolFailureFixture {
  // Every failure pairs by name with a fixture above, so the read is guarded for the type alone.
  const fixture = FIXTURES[name]
  return {
    payload: toolFrame(name, { status: MIMO_TOOL_STATUS.Error, input: INPUTS.get(name) ?? {}, error }),
    ...(fixture?.options !== undefined ? { options: fixture.options } : {}),
    kind,
    name,
    status,
  }
}

/**
 * The MiMo tools that take the uncategorized row on purpose, with the reason.
 *
 * The table the vocabulary test walks is the GENERATED `MIMO_TOOL`, whose values come
 * from `contracts/mimo-protocol.json` -- the Go worker reads the same names.
 */
export const MIMO_GENERIC_TOOLS: Readonly<Record<string, string>> = {
  [MIMO_TOOL.Invalid]: 'MiMo writes this call for a tool name that exists nowhere, or for arguments that broke their tool\'s schema. The arguments and the error are the whole record.',
}

export const MIMO_TOOL_RESULTS: ToolResultCheck = {
  provider: AgentProvider.MIMO_CODE,
  fixtures: FIXTURES,
  failures: [
    failed('execute', MIMO_TOOL.Bash),
    failed('execute', MIMO_TOOL.Bash, 'declined', 'The user rejected permission to use this specific tool call.'),
    failed('execute', MIMO_TOOL.Bash, 'cancelled', 'Tool execution aborted'),
    failed('read', MIMO_TOOL.Read),
    failed('glob', MIMO_TOOL.Glob),
    failed('grep', MIMO_TOOL.Grep),
    failed('edit', MIMO_TOOL.Edit),
    failed('write', MIMO_TOOL.Write),
    failed('agent', MIMO_TOOL.Actor),
    failed('todo', MIMO_TOOL.Task),
    failed('fetch', MIMO_TOOL.WebFetch),
    failed('web_search', MIMO_TOOL.WebSearch),
    failed('search', MIMO_TOOL.CodeSearch),
    failed('skill', MIMO_TOOL.Skill),
    failed('switch_mode', MIMO_TOOL.PlanExit, 'declined', 'The user dismissed this question'),
    failed('memory', MIMO_TOOL.Memory),
    failed('trigger', MIMO_TOOL.Cron),
    failed('task', MIMO_TOOL.Workflow),
    failed('agents', MIMO_TOOL.Session),
    failed('question', MIMO_TOOL.Question, 'declined', 'The user dismissed this question'),
    failed('other', MIMO_TOOL.Invalid),
  ],
  noFailure: {},
  noResult: {},
  unparsed: {
    [MIMO_TOOL.ViewImage]: 'The sentence beside the picture is not a file body; the picture rides the call.',
  },
}
