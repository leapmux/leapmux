import type { ToolKind } from '~/components/chat/model/toolKind'
import type { ToolFailureFixture, ToolResultCheck, ToolResultFixture } from '~/test-support/toolVocabulary'
import { CLINE_TOOL } from '~/generated/contracts/cline-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { CLINE_REJECTION_SUFFIX } from './protocol'
import { CLINE_TOOL_NAME } from './toolNames'

/** The id every fixture's call carries. */
export const CLINE_FIXTURE_CALL_ID = 'call_fixture_1'

/** The session every fixture's row states. */
const SESSION = '1790258346189_zqp76'

/** The `tool.started` row that states one call, as the worker persists it. */
export function clineToolStartRow(name: string, input: Record<string, unknown>, id = CLINE_FIXTURE_CALL_ID): Record<string, unknown> {
  return { version: 'v1', event: 'tool.started', sessionId: SESSION, payload: { toolCallId: id, toolName: name, input } }
}

/** The `tool.finished` row that ends one call. */
export function clineToolFinishRow(name: string, output: unknown, error?: string, id = CLINE_FIXTURE_CALL_ID): Record<string, unknown> {
  return { version: 'v1', event: 'tool.finished', sessionId: SESSION, payload: { toolCallId: id, toolName: name, output, ...(error !== undefined ? { error } : {}) } }
}

/** A successful result row, paired with the row that states its call. */
function answered(name: string, output: unknown, input: Record<string, unknown>): ToolResultFixture {
  return {
    payload: clineToolFinishRow(name, output),
    options: {
      request: {
        wrapper: null,
        topLevel: null,
        parentObject: clineToolStartRow(name, input),
        rawText: '',
        supplementalContent: undefined,
        messageMetadata: undefined,
      },
    },
  }
}

/** One `{query, result, success}` record, the shape most of Cline's tools answer with. */
function operation(query: string, result: string): Record<string, unknown> {
  return { query, result, success: true }
}

const PATCH = '*** Begin Patch\n*** Update File: /work/a.ts\n@@\n-old\n+new\n*** End Patch'

/**
 * One successful result for every tool the kind table holds.
 *
 * `run_commands`, `ask_question` and `spawn_agent` are the results of probes of the real
 * daemon, with the paths shortened. The others follow the result records of Cline's own
 * tool code in `sdk/packages/core/src/extensions/tools`: most tools answer with a list of
 * `{query, result, success}` records, `editor` and `apply_patch` with one record, and a
 * question and a skill with a string.
 */
const FIXTURES: Readonly<Record<string, ToolResultFixture>> = {
  [CLINE_TOOL.RunCommands]: answered(CLINE_TOOL.RunCommands, [operation('echo probe-bash', 'probe-bash\n')], { commands: ['echo probe-bash'] }),
  [CLINE_TOOL.SpawnAgent]: answered(
    CLINE_TOOL.SpawnAgent,
    { text: 'Subagent result: from-subagent.', iterations: 2, finishReason: 'completed', usage: { inputTokens: 2400, outputTokens: 68 } },
    { systemPrompt: 'You help.', task: 'Run one command and report.' },
  ),
  [CLINE_TOOL.AskQuestion]: answered(CLINE_TOOL.AskQuestion, 'Blue', { question: 'Which color do you prefer?', options: ['Red', 'Blue'] }),
  [CLINE_TOOL_NAME.AskFollowupQuestion]: answered(CLINE_TOOL_NAME.AskFollowupQuestion, 'Yes', { question: 'Go on?', options: ['Yes', 'No'] }),
  [CLINE_TOOL.SwitchToActMode]: answered(CLINE_TOOL.SwitchToActMode, 'You successfully switched to act mode, proceed with the plan.', {}),
  [CLINE_TOOL_NAME.ReadFiles]: answered(CLINE_TOOL_NAME.ReadFiles, [operation('/work/notes.txt', '1 | alpha one\n2 | beta two')], { files: [{ path: '/work/notes.txt' }] }),
  [CLINE_TOOL_NAME.SearchCodebase]: answered(
    CLINE_TOOL_NAME.SearchCodebase,
    [operation('alpha', 'Found 1 result for pattern: alpha\n/work/a.ts:3:7')],
    { queries: ['alpha'] },
  ),
  [CLINE_TOOL_NAME.FetchWebContent]: answered(
    CLINE_TOOL_NAME.FetchWebContent,
    [operation('https://example.com', 'URL: https://example.com\nContent-Type: text/html\n\n--- Content ---\nThe page.')],
    { requests: [{ url: 'https://example.com', prompt: 'Summarize the page.' }] },
  ),
  [CLINE_TOOL_NAME.Editor]: answered(
    CLINE_TOOL_NAME.Editor,
    operation('edit:/work/a.ts', 'Edited /work/a.ts\n```diff\n-1: old\n+1: new\n```'),
    { path: '/work/a.ts', old_text: 'old', new_text: 'new' },
  ),
  [CLINE_TOOL_NAME.ApplyPatch]: answered(
    CLINE_TOOL_NAME.ApplyPatch,
    operation('apply_patch', 'Successfully applied patch to the following files:\n/work/a.ts'),
    { input: PATCH },
  ),
  [CLINE_TOOL_NAME.Skills]: answered(CLINE_TOOL_NAME.Skills, 'Loaded the skill.', { skill: 'release', args: null }),
  [CLINE_TOOL_NAME.SubmitAndExit]: answered(CLINE_TOOL_NAME.SubmitAndExit, 'Submitted.', { summary: 'Fixed the build.', verified: true }),
  [CLINE_TOOL_NAME.Tasks]: answered(CLINE_TOOL_NAME.Tasks, 'Listed 2 scheduled tasks.', { action: 'list' }),
  [CLINE_TOOL_NAME.TeamSpawnTeammate]: answered(CLINE_TOOL_NAME.TeamSpawnTeammate, 'Spawned researcher.', { agentId: 'researcher', rolePrompt: 'You research.' }),
  [CLINE_TOOL_NAME.TeamShutdownTeammate]: answered(CLINE_TOOL_NAME.TeamShutdownTeammate, 'Stopped researcher.', { agentId: 'researcher' }),
  [CLINE_TOOL_NAME.TeamStatus]: answered(CLINE_TOOL_NAME.TeamStatus, 'Team builders: 2 members.', {}),
  [CLINE_TOOL_NAME.TeamCleanup]: answered(CLINE_TOOL_NAME.TeamCleanup, 'The team ended.', {}),
  [CLINE_TOOL_NAME.TeamMissionLog]: answered(CLINE_TOOL_NAME.TeamMissionLog, 'Logged.', { summary: 'Found the bug.' }),
  [CLINE_TOOL_NAME.TeamTask]: answered(CLINE_TOOL_NAME.TeamTask, 'Created task task-1.', { action: 'create', title: 'Find the bug' }),
  [CLINE_TOOL_NAME.TeamRunTask]: answered(CLINE_TOOL_NAME.TeamRunTask, 'Queued run run_1.', { agentId: 'researcher', task: 'Find the bug.' }),
  [CLINE_TOOL_NAME.TeamCancelRun]: answered(CLINE_TOOL_NAME.TeamCancelRun, 'Cancelled run_1.', { runId: 'run_1' }),
  [CLINE_TOOL_NAME.TeamListRuns]: answered(CLINE_TOOL_NAME.TeamListRuns, 'run_1: running', {}),
  [CLINE_TOOL_NAME.TeamAwaitRuns]: answered(CLINE_TOOL_NAME.TeamAwaitRuns, 'run_1: completed', { runIds: ['run_1'] }),
  [CLINE_TOOL_NAME.TeamSendMessage]: answered(CLINE_TOOL_NAME.TeamSendMessage, 'Sent.', { agentId: 'researcher', subject: 'Status', body: 'How far along are you?' }),
  [CLINE_TOOL_NAME.TeamBroadcast]: answered(CLINE_TOOL_NAME.TeamBroadcast, 'Sent to 2 teammates.', { subject: 'Plan', body: 'Split the work.' }),
  [CLINE_TOOL_NAME.TeamReadMailbox]: answered(CLINE_TOOL_NAME.TeamReadMailbox, 'No new messages.', {}),
  [CLINE_TOOL_NAME.TeamCreateOutcome]: answered(CLINE_TOOL_NAME.TeamCreateOutcome, 'Created outcome o1.', { title: 'Report' }),
  [CLINE_TOOL_NAME.TeamAttachOutcomeFragment]: answered(CLINE_TOOL_NAME.TeamAttachOutcomeFragment, 'Attached f1.', { outcomeId: 'o1', section: 'Findings', content: 'The bug.' }),
  [CLINE_TOOL_NAME.TeamReviewOutcomeFragment]: answered(CLINE_TOOL_NAME.TeamReviewOutcomeFragment, 'Reviewed f1.', { outcomeId: 'o1', fragmentId: 'f1', approved: true }),
  [CLINE_TOOL_NAME.TeamFinalizeOutcome]: answered(CLINE_TOOL_NAME.TeamFinalizeOutcome, 'Finalized o1.', { outcomeId: 'o1' }),
  [CLINE_TOOL_NAME.TeamListOutcomes]: answered(CLINE_TOOL_NAME.TeamListOutcomes, 'o1: finalized', {}),
}

/**
 * The sentence every failed fixture carries. Synthetic on purpose: the guard asks about
 * the ladder -- the outcome word, the brand, the kind and the request -- and never about
 * Cline's choice of words.
 */
const ERROR_TEXT = 'The tool reported an error.'

/** The FAILED result of the call one successful fixture already states. */
function failed(kind: ToolKind, name: string, error = ERROR_TEXT, status: ToolFailureFixture['status'] = 'failed'): ToolFailureFixture {
  const fixture = FIXTURES[name]
  return {
    payload: clineToolFinishRow(name, { error }, error),
    ...(fixture?.options !== undefined ? { options: fixture.options } : {}),
    kind,
    name,
    status,
  }
}

export const CLINE_TOOL_RESULTS: ToolResultCheck = {
  provider: AgentProvider.CLINE,
  fixtures: FIXTURES,
  failures: [
    failed('execute', CLINE_TOOL.RunCommands),
    // The reader refused the call, and Cline closes the reason with its own words.
    failed('execute', CLINE_TOOL.RunCommands, `Use the clean target. -- ${CLINE_REJECTION_SUFFIX}`, 'declined'),
    failed('agent', CLINE_TOOL.SpawnAgent),
    failed('question', CLINE_TOOL.AskQuestion),
    failed('switch_mode', CLINE_TOOL.SwitchToActMode, `Split the migration first. -- ${CLINE_REJECTION_SUFFIX}`, 'declined'),
    failed('read', CLINE_TOOL_NAME.ReadFiles),
    failed('grep', CLINE_TOOL_NAME.SearchCodebase),
    failed('fetch', CLINE_TOOL_NAME.FetchWebContent),
    failed('edit', CLINE_TOOL_NAME.Editor),
    failed('edit', CLINE_TOOL_NAME.ApplyPatch),
    failed('skill', CLINE_TOOL_NAME.Skills),
    failed('report', CLINE_TOOL_NAME.SubmitAndExit),
    failed('trigger', CLINE_TOOL_NAME.Tasks),
    failed('agents', CLINE_TOOL_NAME.TeamSpawnTeammate),
    failed('task', CLINE_TOOL_NAME.TeamRunTask),
    failed('message', CLINE_TOOL_NAME.TeamSendMessage),
  ],
  noFailure: {},
  noResult: {},
  unparsed: {},
}
