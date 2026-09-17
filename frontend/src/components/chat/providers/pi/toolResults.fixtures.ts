import type { ToolKind } from '~/components/chat/ir/toolKind'
import type { ToolFailureFixture, ToolResultCheck, ToolResultFixture } from '~/test-support/toolVocabulary'
import { PI_TOOL } from '~/generated/contracts/pi-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { PI_AGENT_TOOL, PI_POWERSHELL_TOOL, PI_SEARCH_TOOL } from './protocol'

/** A successful Pi `tool_execution_end` frame for every tool the kind table holds. */
function end(toolName: string, result: Record<string, unknown>, args: Record<string, unknown>): ToolResultFixture {
  return {
    payload: { type: 'tool_execution_end', toolCallId: 'call', toolName, result, isError: false },
    options: {
      request: {
        wrapper: null,
        topLevel: null,
        parentObject: { type: 'tool_execution_start', toolCallId: 'call', toolName, args },
        rawText: '',
        supplementalContent: undefined,
        messageMetadata: undefined,
      },
    },
  }
}

const text = (value: string) => [{ type: 'text', text: value }]

const FIXTURES: Readonly<Record<string, ToolResultFixture>> = {
  [PI_TOOL.Bash]: end(PI_TOOL.Bash, { content: text('ok') }, { command: 'ls' }),
  [PI_POWERSHELL_TOOL]: end(PI_POWERSHELL_TOOL, { content: text('ok') }, { command: 'Get-ChildItem' }),
  [PI_TOOL.Read]: end(PI_TOOL.Read, { content: text('file body') }, { path: '/p/a.ts' }),
  [PI_TOOL.Write]: end(PI_TOOL.Write, { content: text('Written'), details: { diff: '+new' } }, { path: '/p/a.ts', content: 'new' }),
  [PI_TOOL.Edit]: end(PI_TOOL.Edit, { content: text('Edited'), details: { diff: '-old\n+new' } }, { path: '/p/a.ts', edits: [{ oldText: 'old', newText: 'new' }] }),
  [PI_TOOL.Todo]: end(PI_TOOL.Todo, { content: text('Saved'), details: { action: 'list', params: { action: 'list' }, tasks: [{ id: 1, subject: 'One', status: 'pending' }] } }, { action: 'list' }),
  [PI_TOOL.Agent]: end(PI_TOOL.Agent, { content: text('done'), details: { status: 'completed', agentId: 'child', toolUses: 1, durationMs: 10 } }, { description: 'Probe', prompt: 'Run.' }),
  [PI_SEARCH_TOOL.Grep]: end(PI_SEARCH_TOOL.Grep, { content: text('a.ts:1:x') }, { pattern: 'x' }),
  [PI_SEARCH_TOOL.Find]: end(PI_SEARCH_TOOL.Find, { content: text('a.ts') }, { pattern: '*' }),
  [PI_SEARCH_TOOL.List]: end(PI_SEARCH_TOOL.List, { content: text('a.ts') }, { path: '/p' }),
  [PI_TOOL.AskUserQuestion]: end(PI_TOOL.AskUserQuestion, { content: text('answered') }, { questions: [] }),
  [PI_TOOL.PlanQuestion]: end(PI_TOOL.PlanQuestion, { content: text('asked') }, {}),
  [PI_TOOL.GoalQuestion]: end(PI_TOOL.GoalQuestion, { content: text('asked') }, {}),
  [PI_TOOL.GoalQuestionnaire]: end(PI_TOOL.GoalQuestionnaire, { content: text('asked') }, {}),
  [PI_AGENT_TOOL.GetResult]: end(PI_AGENT_TOOL.GetResult, { content: text('report') }, { agent_id: 'child' }),
  [PI_AGENT_TOOL.Steer]: end(PI_AGENT_TOOL.Steer, { content: text('sent') }, { agent_id: 'child', message: 'hi' }),
}

/**
 * The sentence every failed fixture carries.
 *
 * Synthetic on purpose. Pi's own error wording is not confirmable from this
 * repository, and the guard asks about the LADDER -- the outcome word, the brand, the
 * kind and the request -- rather than about any provider's choice of words.
 */
const ERROR_TEXT = 'The tool reported an error.'

/**
 * The FAILED frame of the call one successful fixture already states.
 *
 * Pi flags a failure with `isError` on the `tool_execution_end` frame, and the result
 * then carries the reason in place of the payload.
 *
 * The request half comes from the successful fixture rather than from a second copy of
 * the `tool_execution_start`. The two frames then describe ONE call, which is what lets
 * the ladder assert that a failure keeps the kind, the tool and the request of its
 * success.
 */
function failed(kind: ToolKind, name: string, status: ToolFailureFixture['status'] = 'failed'): ToolFailureFixture {
  // Every failure pairs by name with a fixture above, so the read is guarded for the type alone.
  const fixture = FIXTURES[name]
  return {
    payload: { type: 'tool_execution_end', toolCallId: 'call', toolName: name, result: { content: text(ERROR_TEXT) }, isError: true },
    ...(fixture?.options !== undefined ? { options: fixture.options } : {}),
    kind,
    name,
    status,
  }
}

export const PI_TOOL_RESULTS: ToolResultCheck = {
  provider: AgentProvider.PI,
  fixtures: FIXTURES,
  failures: [
    failed('execute', PI_TOOL.Bash),
    failed('read', PI_TOOL.Read),
    failed('write', PI_TOOL.Write),
    failed('edit', PI_TOOL.Edit),
    failed('todo', PI_TOOL.Todo),
    failed('agent', PI_TOOL.Agent),
    failed('grep', PI_SEARCH_TOOL.Grep),
    failed('glob', PI_SEARCH_TOOL.Find),
    failed('list', PI_SEARCH_TOOL.List),
    failed('question', PI_TOOL.AskUserQuestion),
  ],
  noFailure: {},
  noResult: {
    [PI_TOOL.SubagentWorkflow]: 'A workflow launch states itself on its request row; its result arrives as a notification.',
    [PI_TOOL.PlanComplete]: 'A row with a plan draws through the shared plan card; only a plan-less result reaches the tool path.',
  },
  unparsed: {
    [PI_TOOL.Write]: 'A diff in a format this build cannot parse stays unparsed; the row draws the raw words.',
    [PI_TOOL.Edit]: 'A diff in a format this build cannot parse stays unparsed; the row draws the raw words.',
  },
}
