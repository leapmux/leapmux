import type { ToolSpanRowRole } from '../../../model/row'
import type { ToolCall, ToolCallEnvelope, ToolCallLifecycleFacts, ToolCallSpecReaderTable, ToolCallSpecVariant, UnparsedToolResult } from '../../../model/toolCall'
import type { ToolKind } from '../../../model/toolKind'
import type { ProviderToolOutcome } from '../../../model/toolOutcome'
import type { ToolRequestByKind } from '../../../model/tools'
import type { ToolRequestOverrides } from '../../defaultToolRequests'
import type { ClineToolFinish, ClineToolStart } from './toolCommon'
import type { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { CLINE_TOOL } from '~/generated/contracts/cline-protocol'
import { isObject, pickString } from '~/lib/jsonPick'
import { createToolCall } from '../../../model/createToolCall'
import { parseMcpContentItem, parseMcpToolName } from '../../../model/mcpToolCall'
import { failedResult, proseResult, readToolCallSpec, unparsedResult } from '../../../model/toolCall'
import { toolRequestFor } from '../../defaultToolRequests'
import { retainedOutcome, retainedRowIsFinal } from '../../registry'
import { CLINE_REJECTION_SUFFIX } from '../protocol'
import { clineToolKind } from '../toolKinds'
import { CLINE_TOOL_NAME } from '../toolNames'
import { clineAgentRequest, clineAgentResult } from './agent'
import { clineCommandRequest, clineCommandResults } from './execute'
import { clineEditRequest, clineEditResult } from './fileEdit'
import { clineQuestionRequest, clineQuestionResult } from './question'
import { clineReadRequest, clineReadResult } from './read'
import { clineSearchRequest, clineSearchResult } from './search'
import { clineOperations, clineToolFinish, clineToolStart, outputText } from './toolCommon'

/** The display name of a tool whose wire name is not the word a reader wants. */
const CLINE_TOOL_LABELS: ReadonlyMap<string, string> = new Map<string, string>([
  [CLINE_TOOL.RunCommands, 'Commands'],
  [CLINE_TOOL.SpawnAgent, 'Subagent'],
  [CLINE_TOOL.AskQuestion, 'Question'],
  [CLINE_TOOL_NAME.AskFollowupQuestion, 'Question'],
  [CLINE_TOOL.SwitchToActMode, 'Switch to Act Mode'],
  [CLINE_TOOL_NAME.ReadFiles, 'Read'],
  [CLINE_TOOL_NAME.SearchCodebase, 'Search'],
  [CLINE_TOOL_NAME.FetchWebContent, 'Web Page'],
  [CLINE_TOOL_NAME.Editor, 'Edit'],
  [CLINE_TOOL_NAME.ApplyPatch, 'Apply Patch'],
  [CLINE_TOOL_NAME.Skills, 'Skill'],
  [CLINE_TOOL_NAME.SubmitAndExit, 'Submit'],
  [CLINE_TOOL_NAME.Tasks, 'Scheduled Tasks'],
  [CLINE_TOOL_NAME.TeamSpawnTeammate, 'Start Teammate'],
  [CLINE_TOOL_NAME.TeamShutdownTeammate, 'Stop Teammate'],
  [CLINE_TOOL_NAME.TeamStatus, 'Team Status'],
  [CLINE_TOOL_NAME.TeamCleanup, 'End Team'],
  [CLINE_TOOL_NAME.TeamMissionLog, 'Mission Log'],
  [CLINE_TOOL_NAME.TeamTask, 'Team Task'],
  [CLINE_TOOL_NAME.TeamRunTask, 'Run Team Task'],
  [CLINE_TOOL_NAME.TeamCancelRun, 'Cancel Team Run'],
  [CLINE_TOOL_NAME.TeamListRuns, 'Team Runs'],
  [CLINE_TOOL_NAME.TeamAwaitRuns, 'Await Team Runs'],
  [CLINE_TOOL_NAME.TeamSendMessage, 'Team Message'],
  [CLINE_TOOL_NAME.TeamBroadcast, 'Team Broadcast'],
  [CLINE_TOOL_NAME.TeamReadMailbox, 'Team Mailbox'],
  [CLINE_TOOL_NAME.TeamCreateOutcome, 'Create Outcome'],
  [CLINE_TOOL_NAME.TeamAttachOutcomeFragment, 'Add to Outcome'],
  [CLINE_TOOL_NAME.TeamReviewOutcomeFragment, 'Review Outcome'],
  [CLINE_TOOL_NAME.TeamFinalizeOutcome, 'Finish Outcome'],
  [CLINE_TOOL_NAME.TeamListOutcomes, 'Outcomes'],
])

/**
 * How Cline says a call ended, from its result: declined, failed, or null for a
 * result.
 *
 * Cline ends a call that the reader refused with an error that closes with its own
 * rejection words, so the model reads the refusal as the reader's choice rather than
 * a fault. Every other error is a failure.
 */
export function clineResultOutcome(result: ClineToolFinish | undefined): ProviderToolOutcome | null {
  if (!result || !result.error)
    return null
  return result.error.trimEnd().endsWith(CLINE_REJECTION_SUFFIX) ? 'declined' : 'failed'
}

/**
 * The words of a call's error without Cline's rejection words, which address the model
 * rather than the reader.
 */
export function clineErrorWords(error: string): string {
  const text = error.trimEnd()
  if (!text.endsWith(CLINE_REJECTION_SUFFIX))
    return text
  return text.slice(0, -CLINE_REJECTION_SUFFIX.length).replace(/\s*--\s*$/, '').trim()
}

/** One Cline tool call, resolved from its row and the two rows that describe it. */
export interface ClineToolRow {
  call: ClineToolStart
  /** The call's result, from this row or from the paired result row. */
  result: ClineToolFinish | undefined
  /** True when this row is the last one of its call. */
  finished: boolean
}

/**
 * Build one tool row, or null for a row that is not a Cline tool row.
 *
 * Only a side of THIS call counts: one message can run several calls, and a sibling's
 * row is no side of this one. A result row whose request the store did not resolve
 * still draws, with the tool its own row states.
 */
export function clineToolRow(
  payload: Record<string, unknown> | undefined,
  request: ParsedMessageContent | undefined,
  result: ParsedMessageContent | undefined,
  completion?: MessageCompletion,
): ClineToolRow | null {
  const ownCall = clineToolStart(payload)
  if (ownCall) {
    const paired = clineToolFinish(result?.parentObject)
    return {
      call: ownCall,
      result: paired?.id === ownCall.id ? paired : undefined,
      // A retained start is the call's END: the turn stopped before its result.
      finished: retainedRowIsFinal(completion),
    }
  }
  const ownResult = clineToolFinish(payload)
  if (!ownResult)
    return null
  const paired = clineToolStart(request?.parentObject)
  return {
    call: paired?.id === ownResult.id ? paired : { id: ownResult.id, name: ownResult.name, input: {} },
    result: ownResult,
    finished: true,
  }
}

/** Which SIDE of its span one row draws: the request while the call runs, the answer once it finished. */
export function clineToolSpanRowRole(row: ClineToolRow): ToolSpanRowRole {
  return row.finished ? 'result' : 'request'
}

/** Everything one payload decision reads, collected ONCE for the row. */
export interface ClineToolFacts {
  callId: string
  toolName: string
  label: string | undefined
  args: Record<string, unknown>
  /** The tool's result, or undefined before it landed. */
  output: unknown
  /** The words of the call's error, without Cline's rejection words, or ''. */
  error: string
  /** Cline refused or failed the call, so `error` states why. */
  failed: boolean
  finished: boolean
  lifecycle: ToolCallLifecycleFacts
  resultAvailable: boolean
}

/** Collect everything the payload decisions read, in one pass. */
export function clineToolFacts(row: ClineToolRow, completion: MessageCompletion | undefined): ClineToolFacts {
  const outcome = clineResultOutcome(row.result)
  const toolName = row.call.name
  return {
    callId: row.call.id,
    toolName,
    label: CLINE_TOOL_LABELS.get(toolName) ?? (toolName || undefined),
    args: row.call.input,
    output: row.result?.output,
    error: clineErrorWords(row.result?.error ?? ''),
    failed: outcome !== null,
    finished: row.finished,
    lifecycle: {
      frameStatus: 'unstated',
      providerOutcome: outcome,
      retainedOutcome: retainedOutcome(completion),
      rowFinal: row.finished,
      resultFrameLanded: row.result !== undefined,
    },
    resultAvailable: row.result !== undefined,
  }
}

/**
 * The kind one row draws with. A tool the table does not list -- a tool of a Model
 * Context Protocol server or of a later Cline -- takes the generic card rather than the
 * uncategorized kind.
 */
export function clineToolCallKind(facts: ClineToolFacts): ToolKind {
  const declared = clineToolKind(facts.toolName)
  return declared === 'unspecified' ? 'mcp' : declared
}

/**
 * The kinds Cline reads from its OWN arguments. Every other kind takes the shared
 * `DEFAULT_TOOL_REQUESTS` entry, which reads the arguments alone.
 */
export const CLINE_TOOL_REQUEST_OVERRIDES: ToolRequestOverrides<ClineToolFacts> = {
  // `spawn_agent` states its task, and the call's id is the key of the registry row.
  agent: (args, facts): ToolRequestByKind['agent'] => clineAgentRequest(args, facts.callId),
  // `run_commands` states a list of commands, each a string or a program with its arguments.
  execute: (args): ToolRequestByKind['execute'] => clineCommandRequest(args),
  // `read_files` states a list of files, each with an optional line range.
  read: (args): ToolRequestByKind['read'] => clineReadRequest(args),
  // `search_codebase` states a list of regular expressions.
  grep: (args): ToolRequestByKind['grep'] => clineSearchRequest(args),
  // `editor` states `old_text` and `new_text`, and `apply_patch` a `*** Begin Patch` text.
  edit: (args, facts): ToolRequestByKind['edit'] => clineEditRequest(facts.toolName, args),
  // `fetch_web_content` states a list of requests, each a URL with a prompt.
  fetch: (args): ToolRequestByKind['fetch'] => {
    const requests = Array.isArray(args.requests) ? args.requests.filter(isObject) : []
    return { url: requests.map(request => pickString(request, 'url')).filter(Boolean).join(', ') }
  },
  // `ask_question` states one question and its options.
  question: (args): ToolRequestByKind['question'] => clineQuestionRequest(args),
  // `switch_to_act_mode` states nothing, and its target is Act mode. A refusal of the
  // plan tool is the reader's answer to the plan, not a failure, so the header words it
  // as the plan's rejection.
  switch_mode: (): ToolRequestByKind['switch_mode'] => ({ mode: 'Act', declinedTitle: 'Plan rejected' }),
  // `team_send_message` states its teammate and its text; `team_broadcast` its text.
  message: (args): ToolRequestByKind['message'] => {
    const to = pickString(args, 'agentId') || pickString(args, 'to')
    const summary = pickString(args, 'subject')
    return { ...(to ? { to } : {}), text: pickString(args, 'body') || pickString(args, 'message') || pickString(args, 'text'), ...(summary ? { summary } : {}) }
  },
  // A team tool names its teammate `agentId`.
  agents: (args): ToolRequestByKind['agents'] => {
    const query = pickString(args, 'agentId') || pickString(args, 'name')
    return query ? { query } : {}
  },
  // A team run states its run id, or the task it runs.
  task: (args, facts): ToolRequestByKind['task'] => {
    const taskId = pickString(args, 'runId') || pickString(args, 'taskId')
    const action = facts.toolName === CLINE_TOOL_NAME.TeamCancelRun
      ? 'stop'
      : facts.toolName === CLINE_TOOL_NAME.TeamAwaitRuns
        ? 'output'
        : facts.toolName === CLINE_TOOL_NAME.TeamListRuns ? 'list' : 'other'
    return { action, ...(taskId ? { taskId } : {}) }
  },
  // A tool of a Model Context Protocol server spells its server and tool in its name.
  mcp: (args, facts): ToolRequestByKind['mcp'] => {
    const parsed = parseMcpToolName(facts.toolName)
    return { server: parsed?.server ?? '', tool: parsed?.tool ?? facts.toolName, args }
  },
}

/** One kind's declared request: Cline's own reading where it states one, the shared table's elsewhere. */
function requestFor<K extends ToolKind>(kind: K, facts: ClineToolFacts): ToolRequestByKind[K] {
  return toolRequestFor(kind, facts.args, facts, CLINE_TOOL_REQUEST_OVERRIDES)
}

/** The words above one tool row: the tool's display name, and a title. */
interface ClineRowHeader {
  label?: string
  title?: string
}

/** The tool's display name and the words above it. */
function header(facts: ClineToolFacts): ClineRowHeader {
  return { ...(facts.label !== undefined ? { label: facts.label } : {}), title: facts.label ?? 'Tool' }
}

/**
 * The spec of a call that has no result yet, or that Cline refused or failed, or null
 * for a call whose result the kind reads.
 */
function unfinished<K extends ToolKind>(
  kind: K,
  facts: ClineToolFacts,
  request: ToolRequestByKind[K],
  head: ClineRowHeader,
): ToolCallSpecVariant<K> | null {
  if (!facts.resultAvailable)
    return { kind, ...head, request }
  if (facts.failed)
    return { kind, ...head, request, result: failedResult(facts.error) }
  return null
}

/** The words of a finished call that no reader of its kind reads, or none for an empty result. */
function unreadResult(facts: ClineToolFacts): UnparsedToolResult | undefined {
  const text = outputText(facts.output)
  return text ? unparsedResult(text) : undefined
}

/** The result of a kind that answers in prose: the operations' words, or the result's own text. */
function proseOf(facts: ClineToolFacts, format: 'plain' | 'markdown' = 'plain') {
  const operations = clineOperations(facts.output)
  const text = operations.length > 0
    ? operations.map(operation => operation.error || operation.result).filter(Boolean).join('\n\n')
    : outputText(facts.output)
  return proseResult(text, format)
}

/** The declared payload of a kind Cline never produces: its request, and no result. */
function declaredOnly<K extends ToolKind>(kind: K): (facts: ClineToolFacts) => ToolCallSpecVariant<K> {
  return (facts): ToolCallSpecVariant<K> => ({ kind, ...header(facts), request: requestFor(kind, facts) })
}

/**
 * One reader for each kind, each checked against its OWN kind's request and result.
 *
 * Every entry declares its own return type, because a contextual signature is not an
 * annotated position: without it an entry takes a stray key without a word.
 */
export const CLINE_TOOL_READERS: ToolCallSpecReaderTable<ClineToolFacts> = {
  agent: (facts): ToolCallSpecVariant<'agent'> => {
    const request = requestFor('agent', facts)
    // The subagent's task heads the row, because the tool name says only that a
    // subagent ran.
    const head: ClineRowHeader = { ...header(facts), title: request.description }
    return unfinished('agent', facts, request, head)
      ?? { kind: 'agent', ...head, request, result: clineAgentResult(request, facts.output) }
  },
  execute: (facts): ToolCallSpecVariant<'execute'> => {
    const request = requestFor('execute', facts)
    // No title: the command is the header on every surface.
    const head: ClineRowHeader = facts.label !== undefined ? { label: facts.label } : {}
    return unfinished('execute', facts, request, head)
      ?? { kind: 'execute', ...head, request, result: { commands: clineCommandResults(facts.output), unresolvedTerminals: [] } }
  },
  read: (facts): ToolCallSpecVariant<'read'> => {
    const request = requestFor('read', facts)
    const head = header(facts)
    const early = unfinished('read', facts, request, head)
    if (early)
      return early
    const result = clineReadResult(facts.output)
    if (result)
      return { kind: 'read', ...head, request, result, images: [] }
    const unread = unreadResult(facts)
    return { kind: 'read', ...head, request, ...(unread !== undefined ? { result: unread } : {}) }
  },
  grep: (facts): ToolCallSpecVariant<'grep'> => {
    const request = requestFor('grep', facts)
    const head = header(facts)
    return unfinished('grep', facts, request, head) ?? { kind: 'grep', ...head, request, result: clineSearchResult(facts.output) }
  },
  fetch: (facts): ToolCallSpecVariant<'fetch'> => {
    const request = requestFor('fetch', facts)
    const head = header(facts)
    return unfinished('fetch', facts, request, head) ?? { kind: 'fetch', ...head, request, result: { result: proseOf(facts).text } }
  },
  edit: (facts): ToolCallSpecVariant<'edit'> => {
    const request = requestFor('edit', facts)
    const head = header(facts)
    const early = unfinished('edit', facts, request, head)
    if (early)
      return early
    const landed = clineEditResult(request, facts.output)
    if (landed)
      return { kind: 'edit', ...head, request, result: landed }
    const unread = unreadResult(facts)
    return { kind: 'edit', ...head, request, ...(unread !== undefined ? { result: unread } : {}) }
  },
  question: (facts): ToolCallSpecVariant<'question'> => {
    const request = requestFor('question', facts)
    const head = header(facts)
    return unfinished('question', facts, request, head)
      ?? { kind: 'question', ...head, request, result: clineQuestionResult(request, facts.output) }
  },
  switch_mode: (facts): ToolCallSpecVariant<'switch_mode'> => {
    const request = requestFor('switch_mode', facts)
    const head = header(facts)
    return unfinished('switch_mode', facts, request, head) ?? { kind: 'switch_mode', ...head, request, result: proseOf(facts) }
  },
  skill: (facts): ToolCallSpecVariant<'skill'> => {
    const request = requestFor('skill', facts)
    const head = header(facts)
    return unfinished('skill', facts, request, head) ?? { kind: 'skill', ...head, request, result: proseOf(facts, 'markdown') }
  },
  report: (facts): ToolCallSpecVariant<'report'> => {
    const request = requestFor('report', facts)
    const head = header(facts)
    return unfinished('report', facts, request, head) ?? { kind: 'report', ...head, request, result: proseOf(facts, 'markdown') }
  },
  trigger: (facts): ToolCallSpecVariant<'trigger'> => {
    const request = requestFor('trigger', facts)
    const head = header(facts)
    return unfinished('trigger', facts, request, head) ?? { kind: 'trigger', ...head, request, result: proseOf(facts) }
  },
  agents: (facts): ToolCallSpecVariant<'agents'> => {
    const request = requestFor('agents', facts)
    const head = header(facts)
    return unfinished('agents', facts, request, head) ?? { kind: 'agents', ...head, request, result: proseOf(facts) }
  },
  message: (facts): ToolCallSpecVariant<'message'> => {
    const request = requestFor('message', facts)
    const head = header(facts)
    return unfinished('message', facts, request, head) ?? { kind: 'message', ...head, request, result: proseOf(facts) }
  },
  task: (facts): ToolCallSpecVariant<'task'> => {
    const request = requestFor('task', facts)
    const head = header(facts)
    return unfinished('task', facts, request, head)
      ?? { kind: 'task', ...head, request, result: { outcome: 'completed', output: proseOf(facts).text } }
  },
  mcp: (facts): ToolCallSpecVariant<'mcp'> => {
    const request = requestFor('mcp', facts)
    const head = header(facts)
    if (!facts.resultAvailable)
      return { kind: 'mcp', ...head, request }
    const text = facts.failed ? facts.error : outputText(facts.output)
    return {
      kind: 'mcp',
      ...head,
      request,
      result: {
        content: [parseMcpContentItem({ type: 'text', text })],
        ...(facts.failed ? { error: facts.error } : {}),
      },
    }
  },
  // The kinds Cline states no tool for. Each still declares its own request, so the day
  // one of them arrives it draws its own card rather than a dump.
  unspecified: declaredOnly('unspecified'),
  chart: declaredOnly('chart'),
  delete: declaredOnly('delete'),
  glob: declaredOnly('glob'),
  image: declaredOnly('image'),
  list: declaredOnly('list'),
  memory: declaredOnly('memory'),
  move: declaredOnly('move'),
  other: declaredOnly('other'),
  search: declaredOnly('search'),
  think: declaredOnly('think'),
  todo: declaredOnly('todo'),
  wait: declaredOnly('wait'),
  web_search: declaredOnly('web_search'),
  write: declaredOnly('write'),
}

/** One Cline tool call, as the kind-discriminated pair. */
export function clineToolCall(row: ClineToolRow, completion?: MessageCompletion): ToolCall {
  const facts = clineToolFacts(row, completion)
  const envelope: ToolCallEnvelope = { id: facts.callId, name: facts.toolName, lifecycle: facts.lifecycle }
  return createToolCall(envelope, readToolCallSpec(CLINE_TOOL_READERS, clineToolCallKind(facts), facts))
}
