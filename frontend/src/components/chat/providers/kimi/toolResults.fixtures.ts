import type { ToolKind } from '~/components/chat/model/toolKind'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ToolFailureFixture, ToolResultCheck, ToolResultFixture } from '~/test-support/toolVocabulary'
import { KIMI_TOOL } from '~/generated/contracts/kimi-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { kimiToolResult, kimiToolStart } from '~/test-support/kimiFixtures'
import { input } from '../testUtils'

const CALL = 'call_1'

/** The paired start frame a fixture's result reads its name and arguments from. */
function request(toolName: string, args: Record<string, unknown>, display?: Record<string, unknown>): ParsedMessageContent {
  return input(kimiToolStart(CALL, toolName, args, display), undefined, AgentProvider.KIMI_CODE)
}

/** A successful result for one call, with its paired start beside it. */
function done(toolName: string, args: Record<string, unknown>, output: unknown, display?: Record<string, unknown>): ToolResultFixture {
  return { payload: kimiToolResult(CALL, output), options: { request: request(toolName, args, display), spanType: toolName } }
}

const FIXTURES: Readonly<Record<string, ToolResultFixture>> = {
  [KIMI_TOOL.Bash]: done(KIMI_TOOL.Bash, { command: 'echo hi', description: 'Say hi' }, 'hi\n', { kind: 'command', command: 'echo hi', cwd: '/work', language: 'bash' }),
  [KIMI_TOOL.Read]: done(KIMI_TOOL.Read, { path: 'a.go' }, '1\tpackage a\n2\t\n<system>2 lines read from file.</system>', { kind: 'file_io', operation: 'read', path: '/work/a.go' }),
  [KIMI_TOOL.ReadMediaFile]: done(KIMI_TOOL.ReadMediaFile, { path: 'a.png' }, [{ type: 'text', text: 'Read image a.png' }, { type: 'image_url', imageUrl: { url: 'data:image/png;base64,iVBORw0KGgo=' } }]),
  [KIMI_TOOL.Write]: done(KIMI_TOOL.Write, { path: 'goal.txt', content: 'ok\n' }, 'Wrote 3 bytes to goal.txt', { kind: 'file_io', operation: 'write', path: '/work/goal.txt' }),
  [KIMI_TOOL.Edit]: done(KIMI_TOOL.Edit, { path: 'a.go', old_string: 'x', new_string: 'y' }, 'Edited a.go'),
  [KIMI_TOOL.Glob]: done(KIMI_TOOL.Glob, { pattern: '*.go' }, 'a.go\nb.go'),
  [KIMI_TOOL.Grep]: done(KIMI_TOOL.Grep, { pattern: 'needle' }, 'a.go:3:needle here'),
  [KIMI_TOOL.FetchURL]: done(KIMI_TOOL.FetchURL, { url: 'https://example.com' }, '# Example'),
  [KIMI_TOOL.WebSearch]: done(KIMI_TOOL.WebSearch, { query: 'kimi' }, 'Kimi Code is a coding agent.'),
  [KIMI_TOOL.TodoList]: done(KIMI_TOOL.TodoList, { todos: [{ title: 'Run echo', status: 'in_progress' }] }, 'Todo list updated.'),
  [KIMI_TOOL.Agent]: done(KIMI_TOOL.Agent, { prompt: 'List the files.', description: 'Probe subagent', subagent_type: 'explore' }, 'agent_id: agent-0\nactual_subagent_type: explore\nstatus: completed\nstop_reason: completed\n\n[summary]\nSubagent finished.\n\nresume_hint: Continue with Agent(resume="agent-0", prompt="...").'),
  [KIMI_TOOL.AgentSwarm]: done(KIMI_TOOL.AgentSwarm, { description: 'Probe swarm', prompt_template: 'Reply with {{item}}.', items: ['alpha', 'beta'] }, '<agent_swarm_result>\n<summary>completed: 2</summary>\n<subagent agent_id="agent-0" item="alpha" outcome="completed">alpha done</subagent>\n<subagent agent_id="agent-1" item="beta" outcome="completed">beta done</subagent>\n</agent_swarm_result>'),
  [KIMI_TOOL.AskUserQuestion]: done(KIMI_TOOL.AskUserQuestion, { questions: [{ question: 'Which color?', options: [{ label: 'Red' }, { label: 'Blue' }] }] }, '{"answers":{"Which color?":"Red"}}'),
  [KIMI_TOOL.Skill]: done(KIMI_TOOL.Skill, { skill: 'deploy' }, 'Deployed.'),
  [KIMI_TOOL.TaskList]: done(KIMI_TOOL.TaskList, {}, 'bash-1 running'),
  [KIMI_TOOL.TaskOutput]: done(KIMI_TOOL.TaskOutput, { task_id: 'bash-1' }, 'status: running\noutput: hi'),
  [KIMI_TOOL.TaskStop]: done(KIMI_TOOL.TaskStop, { task_id: 'bash-1' }, 'Stopped bash-1.'),
  [KIMI_TOOL.WaitFor]: done(KIMI_TOOL.WaitFor, { timeout: 30 }, 'Task bash-1 completed.'),
  [KIMI_TOOL.EnterPlanMode]: done(KIMI_TOOL.EnterPlanMode, {}, 'Entered plan mode. Plan file: /work/plan.md'),
  [KIMI_TOOL.CronCreate]: done(KIMI_TOOL.CronCreate, { cron: '0 9 * * *', prompt: 'Check the build.' }, 'Created cron job cron-1.'),
  [KIMI_TOOL.CronList]: done(KIMI_TOOL.CronList, {}, 'cron-1: 0 9 * * *'),
  [KIMI_TOOL.CronDelete]: done(KIMI_TOOL.CronDelete, { id: 'cron-1' }, 'Deleted cron-1.'),
  [KIMI_TOOL.CreateGoal]: done(KIMI_TOOL.CreateGoal, { objective: 'Ship it' }, '{"goalId":"goal_1"}'),
  [KIMI_TOOL.GetGoal]: done(KIMI_TOOL.GetGoal, {}, '{"objective":"Ship it","status":"active"}'),
  [KIMI_TOOL.UpdateGoal]: done(KIMI_TOOL.UpdateGoal, { status: 'complete' }, '{"status":"complete"}'),
  [KIMI_TOOL.SetGoalBudget]: done(KIMI_TOOL.SetGoalBudget, { value: 5, unit: 'turns' }, 'Budget set to 5 turns.'),
  [KIMI_TOOL.NotifyUser]: done(KIMI_TOOL.NotifyUser, { message: 'Build finished.' }, 'Notified.'),
}

/**
 * The sentence every failed fixture carries.
 *
 * Synthetic on purpose: the guard asks about the ladder -- the outcome word, the kind
 * and the request -- and never about the wording the server chooses.
 */
const ERROR_TEXT = 'The tool reported an error.'

/** A FAILED result: `isError` set, and the reason as the output. */
function failed(kind: ToolKind, name: string): ToolFailureFixture {
  const paired = FIXTURES[name]
  if (paired === undefined)
    throw new Error(`No successful fixture pairs with the failed frame for ${name}`)
  return {
    payload: kimiToolResult(CALL, ERROR_TEXT, { isError: true }),
    ...(paired.options !== undefined ? { options: paired.options } : {}),
    kind,
    name,
    status: 'failed',
  }
}

export const KIMI_TOOL_RESULTS: ToolResultCheck = {
  provider: AgentProvider.KIMI_CODE,
  fixtures: FIXTURES,
  failures: [
    failed('execute', KIMI_TOOL.Bash),
    failed('read', KIMI_TOOL.Read),
    failed('write', KIMI_TOOL.Write),
    failed('edit', KIMI_TOOL.Edit),
    failed('glob', KIMI_TOOL.Glob),
    failed('grep', KIMI_TOOL.Grep),
    failed('fetch', KIMI_TOOL.FetchURL),
    failed('web_search', KIMI_TOOL.WebSearch),
    failed('todo', KIMI_TOOL.TodoList),
    failed('agent', KIMI_TOOL.Agent),
    failed('question', KIMI_TOOL.AskUserQuestion),
    failed('skill', KIMI_TOOL.Skill),
    failed('task', KIMI_TOOL.TaskOutput),
    failed('wait', KIMI_TOOL.WaitFor),
    failed('switch_mode', KIMI_TOOL.EnterPlanMode),
    failed('trigger', KIMI_TOOL.CronCreate),
    failed('report', KIMI_TOOL.GetGoal),
    failed('message', KIMI_TOOL.NotifyUser),
  ],
  noFailure: {},
  noResult: {
    [KIMI_TOOL.ExitPlanMode]: 'The call proposes a plan: its start draws as the plan itself and its result row is hidden, because the plan row and the saved answer state the plan and the decision.',
  },
  unparsed: {},
}
