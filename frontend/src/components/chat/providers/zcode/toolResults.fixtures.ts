import type { ToolKind } from '~/components/chat/model/toolKind'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ToolFailureFixture, ToolResultCheck, ToolResultFixture } from '~/test-support/toolVocabulary'
import { ZCODE_TOOL } from '~/generated/contracts/zcode-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../testUtils'

const CALL = 'call-1'

function event(kind: string, fields: Record<string, unknown>): Record<string, unknown> {
  return { type: 'tool.updated', payload: { kind, toolCallId: CALL, ...fields } }
}

/** A finished call: a scheduled frame beside its result. */
function done(toolName: string, args: Record<string, unknown>, result: Record<string, unknown>): ToolResultFixture {
  return {
    payload: event('result', { toolName, result }),
    options: {
      spanType: toolName,
      request: input(event('scheduled', { toolName, input: args })) as ParsedMessageContent,
    },
  }
}

const FIXTURES: Readonly<Record<string, ToolResultFixture>> = {
  [ZCODE_TOOL.Bash]: done(ZCODE_TOOL.Bash, { command: 'ls' }, { success: true, content: 'a.ts' }),
  [ZCODE_TOOL.Read]: done(ZCODE_TOOL.Read, { file_path: '/p/a.ts' }, { success: true, content: '1\talpha' }),
  [ZCODE_TOOL.Write]: done(ZCODE_TOOL.Write, { file_path: '/p/a.ts', content: 'new' }, { success: true, content: 'Written' }),
  [ZCODE_TOOL.Edit]: done(ZCODE_TOOL.Edit, { file_path: '/p/a.ts', old_string: 'x', new_string: 'y' }, { success: true, content: 'Edited' }),
  // A WELL-FORMED envelope, which the shared apply-patch reader accepts. Both halves of
  // this call read it, so the word "Applied" never reaches the row: the request states
  // the change the patch asks for, and the result states the one it applied. This name
  // is therefore absent from `unparsed` below, and an envelope edited into a shape the
  // reader refuses puts it back there -- `undocumentedUnparsedToolResults` says so.
  [ZCODE_TOOL.ApplyPatch]: done(ZCODE_TOOL.ApplyPatch, { patch: '*** Begin Patch\n*** Update File: a.ts\n@@\n-before\n+after\n*** End Patch' }, { success: true, content: 'Applied' }),
  [ZCODE_TOOL.Glob]: done(ZCODE_TOOL.Glob, { pattern: '*.ts' }, { success: true, content: 'a.ts' }),
  [ZCODE_TOOL.Grep]: done(ZCODE_TOOL.Grep, { pattern: 'x' }, { success: true, content: 'a.ts:1:x' }),
  [ZCODE_TOOL.Agent]: done(ZCODE_TOOL.Agent, { description: 'Probe', prompt: 'Run.' }, { success: true, content: 'done' }),
  [ZCODE_TOOL.Task]: done(ZCODE_TOOL.Task, { description: 'Probe', prompt: 'Run.' }, { success: true, content: 'done' }),
  [ZCODE_TOOL.TodoWrite]: done(ZCODE_TOOL.TodoWrite, { todos: [{ content: 'One', status: 'pending' }] }, { success: true, content: 'Saved' }),
  [ZCODE_TOOL.TodoRead]: done(ZCODE_TOOL.TodoRead, { todos: [] }, { success: true, content: 'list' }),
  [ZCODE_TOOL.WebFetch]: done(ZCODE_TOOL.WebFetch, { url: 'https://example.com' }, { success: true, content: '# page' }),
  [ZCODE_TOOL.WebSearch]: done(ZCODE_TOOL.WebSearch, { query: 'q' }, { success: true, content: 'searched' }),
  [ZCODE_TOOL.ServerWebSearch]: done(ZCODE_TOOL.ServerWebSearch, { query: 'q' }, { success: true, content: 'searched' }),
  [ZCODE_TOOL.AskUserQuestion]: done(ZCODE_TOOL.AskUserQuestion, { questions: [{ question: 'Which?' }] }, { success: true, content: 'answered' }),
  [ZCODE_TOOL.GoalRead]: done(ZCODE_TOOL.GoalRead, { goal_id: 'g1' }, { success: true, content: 'goal' }),
  [ZCODE_TOOL.ReadSessionContext]: done(ZCODE_TOOL.ReadSessionContext, {}, { success: true, content: 'context' }),
  [ZCODE_TOOL.Js]: done(ZCODE_TOOL.Js, { code: '1+1' }, { success: true, content: '2' }),
  [ZCODE_TOOL.JsAddNodeModuleDir]: done(ZCODE_TOOL.JsAddNodeModuleDir, { path: '/p' }, { success: true, content: 'added' }),
  [ZCODE_TOOL.JsReset]: done(ZCODE_TOOL.JsReset, {}, { success: true, content: 'reset' }),
  [ZCODE_TOOL.EnterPlanMode]: done(ZCODE_TOOL.EnterPlanMode, {}, { success: true, content: 'entered' }),
  [ZCODE_TOOL.ExitPlanMode]: done(ZCODE_TOOL.ExitPlanMode, {}, { success: true, content: 'exited' }),
  [ZCODE_TOOL.TaskOutput]: done(ZCODE_TOOL.TaskOutput, { task_id: 't1' }, { success: true, content: 'out' }),
  [ZCODE_TOOL.TaskStop]: done(ZCODE_TOOL.TaskStop, { task_id: 't1' }, { success: true, content: 'stopped' }),
  [ZCODE_TOOL.SendMessage]: done(ZCODE_TOOL.SendMessage, { to: 'peer', message: 'hi' }, { success: true, content: 'sent' }),
  [ZCODE_TOOL.RespondToCoordinator]: done(ZCODE_TOOL.RespondToCoordinator, {}, { success: true, content: 'replied' }),
  [ZCODE_TOOL.CronCreate]: done(ZCODE_TOOL.CronCreate, { name: 'nightly' }, { success: true, content: 'created' }),
  [ZCODE_TOOL.CronList]: done(ZCODE_TOOL.CronList, {}, { success: true, content: '[]' }),
  [ZCODE_TOOL.CronUpdate]: done(ZCODE_TOOL.CronUpdate, { id: 'c1' }, { success: true, content: 'updated' }),
  [ZCODE_TOOL.CronDelete]: done(ZCODE_TOOL.CronDelete, { id: 'c1' }, { success: true, content: 'deleted' }),
  [ZCODE_TOOL.Skill]: done(ZCODE_TOOL.Skill, { skill: 'deploy' }, { success: true, content: 'ran' }),
}

/**
 * The sentence every failed fixture carries.
 *
 * Synthetic on purpose. ZCode's own error wording is not confirmable from this
 * repository, and the guard asks about the LADDER -- the outcome word, the brand, the
 * kind and the request -- rather than about any provider's choice of words.
 */
const ERROR_TEXT = 'The tool reported an error.'

/**
 * The FAILED frame of the call one successful fixture already states.
 *
 * The app-server omits `success` on some result shapes, so only an explicit `false`
 * states a failure, and `content` then carries the reason in place of the output.
 *
 * The request half comes from the successful fixture rather than from a second copy of
 * the `scheduled` frame. The two frames then describe ONE call, which is what lets the
 * ladder assert that a failure keeps the kind, the tool and the request of its success.
 */
function failed(kind: ToolKind, name: string, status: ToolFailureFixture['status'] = 'failed'): ToolFailureFixture {
  // Every failure pairs by name with a fixture above, so the read is guarded for the type alone.
  const fixture = FIXTURES[name]
  return {
    payload: event('result', { toolName: name, result: { success: false, content: ERROR_TEXT } }),
    ...(fixture?.options !== undefined ? { options: fixture.options } : {}),
    kind,
    name,
    status,
  }
}

export const ZCODE_TOOL_RESULTS: ToolResultCheck = {
  provider: AgentProvider.ZCODE,
  fixtures: FIXTURES,
  failures: [
    failed('execute', ZCODE_TOOL.Bash),
    failed('read', ZCODE_TOOL.Read),
    failed('write', ZCODE_TOOL.Write),
    failed('edit', ZCODE_TOOL.Edit),
    failed('glob', ZCODE_TOOL.Glob),
    failed('grep', ZCODE_TOOL.Grep),
    failed('agent', ZCODE_TOOL.Agent),
    failed('todo', ZCODE_TOOL.TodoWrite),
    failed('fetch', ZCODE_TOOL.WebFetch),
    failed('web_search', ZCODE_TOOL.WebSearch),
    failed('question', ZCODE_TOOL.AskUserQuestion),
    failed('switch_mode', ZCODE_TOOL.EnterPlanMode),
    failed('task', ZCODE_TOOL.TaskOutput),
    failed('message', ZCODE_TOOL.SendMessage),
    failed('trigger', ZCODE_TOOL.CronCreate),
    failed('skill', ZCODE_TOOL.Skill),
  ],
  noFailure: {},
  noResult: {},
  unparsed: {
    [ZCODE_TOOL.GoalRead]: 'A goal record this build reads only as text.',
    [ZCODE_TOOL.ReadSessionContext]: 'A session context this build reads only as text.',
    [ZCODE_TOOL.Js]: 'A sandbox result this build reads only as text.',
    [ZCODE_TOOL.JsAddNodeModuleDir]: 'A sandbox result this build reads only as text.',
    [ZCODE_TOOL.JsReset]: 'A sandbox result this build reads only as text.',
  },
}
