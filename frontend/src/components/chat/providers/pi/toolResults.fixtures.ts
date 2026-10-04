import type { ToolKind } from '~/components/chat/model/toolKind'
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
  [PI_TOOL.Codemode]: end(PI_TOOL.Codemode, { content: text('Script completed\nOutput:\ncomplete'), details: { calls: [] } }, { code: 'text("complete")' }),
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
 * Use one synthetic error sentence for the shared failure checks.
 *
 * These checks compare the outcome and result brand. They also compare the kind and request.
 * Exact native error text belongs to the captured-frame tests.
 */
const ERROR_TEXT = 'The tool reported an error.'

/**
 * Build a failed completion for a call in the successful corpus.
 *
 * Pi puts `isError` on tool_execution_end and the failure reason in its result.
 * Reuse the successful fixture's start frame. Both completions then belong to one call.
 * The failure checks require the same kind and tool. They require the same request also.
 */
function failed(kind: ToolKind, name: string, status: ToolFailureFixture['status'] = 'failed'): ToolFailureFixture {
  // Each failed call uses its matching successful fixture.
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
    failed('mcp', PI_TOOL.Codemode),
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
    [PI_TOOL.SubagentWorkflow]: 'The request shows the workflow launch. A notification carries its result.',
    [PI_TOOL.PlanComplete]: 'A plan uses the shared plan card. Only a result without a plan uses the tool path.',
  },
  unparsed: {
    [PI_TOOL.Write]: 'The row shows the original text when the parser cannot read the diff format.',
    [PI_TOOL.Edit]: 'The row shows the original text when the parser cannot read the diff format.',
  },
}
