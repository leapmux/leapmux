import type { McpContentItem } from '../../../model/mcpToolCall'
import type { QuestionPrompt } from '../../../model/question'
import type { ToolCall, ToolCallEnvelope, ToolCallSpecReaderTable, ToolCallSpecVariant, ToolFailureResult, UnparsedToolResult } from '../../../model/toolCall'
import type { ToolCallStatus } from '../../../model/toolCallStatus'
import type { ToolKind } from '../../../model/toolKind'
import type { ProviderToolOutcome } from '../../../model/toolOutcome'
import type { ToolRequestByKind } from '../../../model/tools'
import type { GenericToolResult } from '../../../model/tools/generic'
import type { TaskRequest, TaskStatus } from '../../../model/tools/task'
import type { TriggerRequest } from '../../../model/tools/trigger'
import type { ToolRequestOverrides } from '../../defaultToolRequests'
import type { MiMoReadBody } from './read'
import type { MiMoToolPart } from './toolCommon'
import type { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ImageResultSource } from '~/lib/imageBlocks'
import { MIMO_ACTOR_ACTION, MIMO_TOOL, MIMO_TOOL_STATUS } from '~/generated/contracts/mimo-protocol'
import { withFallbackFilePath } from '~/lib/imageBlocks'
import { isObject, pickBoolean, pickNumber, pickString } from '~/lib/jsonPick'
import { createToolCall } from '../../../model/createToolCall'
import { failedResult, proseResult, readToolCallSpec, unparsedResult } from '../../../model/toolCall'
import { toolRequestFor } from '../../defaultToolRequests'
import { questionsFromRecords } from '../../questionRecords'
import { retainedOutcome, retainedRowIsFinal } from '../../registry'
import { MIMO_ABORTED_TOOL_ERROR, MIMO_CRON_ACTION, MIMO_DECLINED_ERRORS, MIMO_PLAN_EXIT_OUTSIDE_PLAN_TITLE, MIMO_WORKFLOW_OPERATION } from '../protocol'
import { mimoToolKind } from '../toolKinds'
import { mimoActorAction, mimoActorOperation, mimoActorRequest, mimoActorRun, mimoActorSpawns, mimoActorTaskOutcome } from './agent'
import { mimoExecOutcome, mimoExecuteRequest, mimoExecuteResult } from './execute'
import { mimoLandedChanges, mimoRequestedChanges } from './fileEdit'
import { parseMiMoRead } from './read'
import { MIMO_SEARCH_EMPTY, mimoGlobFiles, mimoGrepMatches } from './search'
import { mimoTaskRequest, mimoTaskResult } from './todo'
import { mimoToolFinished, mimoToolImages } from './toolCommon'

/**
 * Everything one MiMo call states, collected once before any reader runs.
 *
 * MiMo writes the complete call state on each part update.
 * The row uses its paired final result when that result exists.
 * Otherwise, the row uses its own frame.
 * The request uses the same frame because each frame states the input.
 */
export interface MiMoToolFacts {
  part: MiMoToolPart
  registryKey?: string
  /** The kind this call takes, after {@link mimoCallKind}. */
  kind: ToolKind
  /** The words the call answered with: its output, or its failure. */
  text: string
  /** True when the call reached a final frame: it completed or it ended in an error. */
  answered: boolean
  /**
   * True when the Worker retains the last update of a call that did not answer.
   * That update becomes the final row and preserves the call's recorded activity.
   */
  retained: boolean
  /** True for an error that MiMo did not attribute to the user's refusal or an interruption. */
  failed: boolean
  /** True for a call that the user refused before it ran. */
  declined: boolean
  /** True for a call that an abort cut short. */
  aborted: boolean
  images: ImageResultSource[]
  /** The parsed read body, for a read call that answered in MiMo's own read format. */
  read: MiMoReadBody | null
}

/** What one row states about its call. */
export interface MiMoToolRow {
  /** The row's own tool part. */
  own: MiMoToolPart
  registryKey?: string
  /** The final frame of the same call, when the span holds one beside this row. */
  result?: MiMoToolPart
  /** LeapMux's own reading of how the row ended. */
  completion?: MessageCompletion
  /** True when this row is the last one of its call. */
  rowFinal: boolean
}

/** One MiMo tool call, as the kind-discriminated pair. */
export function mimoToolCall(row: MiMoToolRow): ToolCall {
  const part = row.result && mimoToolFinished(row.result) ? row.result : row.own
  const facts = mimoToolFacts(part, part === row.own && retainedRowIsFinal(row.completion), row.registryKey)
  const envelope: ToolCallEnvelope = {
    id: part.partId,
    name: part.tool,
    lifecycle: {
      frameStatus: frameStatus(part.status),
      providerOutcome: providerOutcome(facts),
      retainedOutcome: retainedOutcome(row.completion),
      rowFinal: row.rowFinal,
      resultFrameLanded: facts.answered,
    },
  }
  const spec = mimoSpecFor(facts)
  // The tool's OWN name, which the icon tooltip states.
  const label = spec.label ?? (part.tool || undefined)
  return createToolCall(envelope, { ...spec, ...(label !== undefined ? { label } : {}) })
}

/** The status word one frame states. */
function frameStatus(status: string): ToolCallStatus {
  switch (status) {
    case MIMO_TOOL_STATUS.Pending:
      return 'pending'
    case MIMO_TOOL_STATUS.Running:
      return 'in_progress'
    case MIMO_TOOL_STATUS.Completed:
      return 'completed'
    case MIMO_TOOL_STATUS.Error:
      return 'failed'
    default:
      return 'unstated'
  }
}

/** How the provider says the call ended, for an ending that is not a plain success. */
function providerOutcome(facts: MiMoToolFacts): ProviderToolOutcome | null {
  if (facts.declined)
    return 'declined'
  if (facts.aborted)
    return 'interrupted'
  if (facts.failed)
    return 'failed'
  return null
}

/**
 * Collect every fact one frame states, and settle the kind the call takes.
 *
 * `retainedFinal` identifies the last update that the Worker retains for a call that did not answer.
 */
export function mimoToolFacts(part: MiMoToolPart, retainedFinal = false, registryKey?: string): MiMoToolFacts {
  const hasErrorStatus = part.status === MIMO_TOOL_STATUS.Error
  const declined = hasErrorStatus && MIMO_DECLINED_ERRORS.some(sentence => part.error.includes(sentence))
  const aborted = hasErrorStatus && !declined && (part.error.includes(MIMO_ABORTED_TOOL_ERROR) || pickBoolean(part.metadata, 'interrupted') === true)
  const answered = mimoToolFinished(part)
  const read = answered && !hasErrorStatus && (part.tool === MIMO_TOOL.Read) ? parseMiMoRead(part.output) : null
  const facts: MiMoToolFacts = {
    part,
    ...(registryKey ? { registryKey } : {}),
    kind: mimoToolKind(part.tool),
    text: hasErrorStatus ? part.error || 'Tool call failed' : part.output,
    answered,
    retained: retainedFinal && !answered,
    failed: hasErrorStatus && !declined && !aborted,
    declined,
    aborted,
    images: mimoToolImages(part),
    read,
  }
  return { ...facts, kind: mimoCallKind(facts) }
}

/**
 * The kind one call takes, after the moves its operation or its answer states.
 *
 * Three tools contain multiple operations. Their input or result selects the kind.
 * Actor operations use these kinds:
 * - A spawn or run uses `agent`.
 * - A send uses `message`.
 * - A status request uses `task`.
 * - A wait uses `task`.
 * - A cancel uses `task`.
 * - A model list uses `task`.
 *
 * A `read` result that contains a directory listing uses `list`.
 * The result distinguishes a file read from a directory read.
 * A file change that specifies no file uses the generic card.
 * An unreadable to-do or actor operation uses that card also.
 * In these cases, the arguments supply the only record of the request.
 */
export function mimoCallKind(facts: MiMoToolFacts): ToolKind {
  const { part } = facts
  switch (part.tool) {
    case MIMO_TOOL.Actor: {
      const action = mimoActorAction(part.input)
      if (mimoActorSpawns(part.input))
        return 'agent'
      if (action === MIMO_ACTOR_ACTION.Send)
        return 'message'
      if (action === MIMO_ACTOR_ACTION.Status || action === MIMO_ACTOR_ACTION.Wait || action === MIMO_ACTOR_ACTION.Cancel || action === MIMO_ACTOR_ACTION.Models)
        return 'task'
      return 'other'
    }
    case MIMO_TOOL.Task:
      return isObject(part.input.operation) ? 'todo' : 'other'
    case MIMO_TOOL.Read:
      return facts.read?.type === 'directory' ? 'list' : 'read'
    default:
      break
  }
  if ((facts.kind === 'edit' || facts.kind === 'write') && mimoRequestedChanges(part.tool, part.input).length === 0)
    return 'other'
  return facts.kind
}

/** The questions a `question` call asked, from its arguments. */
function mimoQuestions(input: Record<string, unknown>): QuestionPrompt[] {
  return questionsFromRecords(
    input.questions,
    (question) => {
      const header = pickString(question, 'header')
      return { ...(header ? { header } : {}), question: pickString(question, 'question') }
    },
    (option) => {
      const label = pickString(option, 'label')
      if (!label)
        return null
      const description = pickString(option, 'description')
      return { label, ...(description ? { description } : {}) }
    },
  )
}

/** The action a `workflow` call or a non-spawning `actor` call takes on its task. */
function mimoTaskAction(part: MiMoToolPart): TaskRequest['action'] {
  const operation = part.tool === MIMO_TOOL.Workflow ? pickString(part.input, 'operation') : mimoActorAction(part.input)
  switch (operation) {
    case MIMO_WORKFLOW_OPERATION.Status:
    case MIMO_WORKFLOW_OPERATION.Wait:
    case MIMO_ACTOR_ACTION.Status:
    case MIMO_ACTOR_ACTION.Wait:
      return 'output'
    case MIMO_WORKFLOW_OPERATION.Cancel:
    case MIMO_ACTOR_ACTION.Cancel:
      return 'stop'
    case MIMO_ACTOR_ACTION.Models:
      return 'list'
    default:
      return 'other'
  }
}

/** The action each `cron` operation takes on its scheduled job. */
const MIMO_CRON_TRIGGER_ACTIONS: ReadonlyMap<string, TriggerRequest['action']> = new Map<string, TriggerRequest['action']>([
  [MIMO_CRON_ACTION.Schedule, 'create'],
  [MIMO_CRON_ACTION.Loop, 'create'],
  [MIMO_CRON_ACTION.List, 'list'],
  [MIMO_CRON_ACTION.Get, 'get'],
  [MIMO_CRON_ACTION.Delete, 'delete'],
  [MIMO_CRON_ACTION.Rename, 'update'],
])

/**
 * The kinds MiMo reads DIFFERENTLY from the shared table, and nothing else.
 *
 * Each entry reads provider facts that the shared entry cannot read:
 * - The nested `operation`.
 * - The tool name.
 * - MiMo's snake-case keys.
 *
 * These other kinds use `DEFAULT_TOOL_REQUESTS`:
 * - `read`.
 * - `glob`.
 * - `grep`.
 * - `fetch`.
 * - `web_search`.
 * - `search`.
 * - `skill`.
 * - `memory`.
 * - `agents`.
 * - `list`.
 * - `other`.
 *
 * Each entry declares its return type, as `DEFAULT_TOOL_REQUESTS` requires.
 * A contextual signature does not supply an annotated position.
 * Without that annotation, an entry can accept an unknown key.
 */
export const MIMO_TOOL_REQUEST_OVERRIDES: ToolRequestOverrides<MiMoToolFacts> = {
  // MiMo nests the launch under `operation`.
  // The Worker uses the native spawn part ID as its registry key.
  agent: (args, facts): ToolRequestByKind['agent'] => ({ ...mimoActorRequest(args), ...(facts.registryKey ? { registryKey: facts.registryKey } : {}) }),
  // The recipient and the words of an `actor send`, nested under `operation`.
  message: (args): ToolRequestByKind['message'] => {
    const operation = mimoActorOperation(args)
    const to = pickString(operation, 'to_actor_id')
    return { ...(to ? { to } : {}), text: pickString(operation, 'content') }
  },
  // The subagent or the workflow run the call acts on, and the action the operation
  // states. The shared entry answers `other` for every call.
  task: (args, facts): ToolRequestByKind['task'] => {
    const taskId = facts.part.tool === MIMO_TOOL.Workflow
      ? pickString(args, 'run_id')
      : pickString(mimoActorOperation(args), 'actor_id') || pickString(mimoActorOperation(args), 'to_actor_id')
    return { action: mimoTaskAction(facts.part), ...(taskId ? { taskId } : {}) }
  },
  // The language and the working directory, which the tool name and MiMo's own
  // `workdir` key state.
  execute: (args, facts): ToolRequestByKind['execute'] => mimoExecuteRequest(facts.part.tool, args),
  // The change each of MiMo's five file tools states in its own shape.
  edit: (args, facts): ToolRequestByKind['edit'] => ({
    changes: mimoRequestedChanges(facts.part.tool, args),
    ...(args.replace_all === true ? { replaceAll: true } : {}),
  }),
  write: (args, facts): ToolRequestByKind['write'] => ({ changes: mimoRequestedChanges(facts.part.tool, args) }),
  // The item the `task` call acted on, nested under `operation`.
  todo: (args): ToolRequestByKind['todo'] => mimoTaskRequest(args),
  // The parsed QUESTIONS. The shared entry states an empty list, because the shape of
  // a question is each provider's own.
  question: (args): ToolRequestByKind['question'] => ({ questions: mimoQuestions(args) }),
  // The job and its schedule, nested under `operation`, and the action its operation
  // word states.
  trigger: (args): ToolRequestByKind['trigger'] => {
    const operation = isObject(args.operation) ? args.operation : {}
    const triggerId = pickString(operation, 'id')
    const schedule = pickString(operation, 'cron')
    const name = pickString(operation, 'prompt')
    return {
      action: MIMO_CRON_TRIGGER_ACTIONS.get(pickString(operation, 'action')) ?? 'other',
      ...(triggerId ? { triggerId } : {}),
      ...(name ? { name } : {}),
      ...(schedule ? { schedule } : {}),
    }
  },
  // A plan approval selects MiMo's build agent.
  // The header states when the user sends the plan back.
  // Outside plan mode, the call changes no mode and keeps MiMo's title.
  switch_mode: (_args, facts): ToolRequestByKind['switch_mode'] =>
    mimoPlanExitOutsidePlanMode(facts) ? {} : { mode: 'build', declinedTitle: 'Plan sent back' },
}

/**
 * True for a `plan_exit` call outside plan mode.
 * MiMo asks no question and reports that plan mode is inactive.
 * It returns `switched: false`, as it does when the user sends a plan back.
 * The native title distinguishes these cases.
 */
function mimoPlanExitOutsidePlanMode(facts: MiMoToolFacts): boolean {
  return facts.answered && facts.part.title === MIMO_PLAN_EXIT_OUTSIDE_PLAN_TITLE
}

/**
 * How a workflow run stands, from the status its `workflow` call states. The workflow
 * tool states words of its own, and the reading of a subagent's status in
 * `mimoActorTaskOutcome` does not apply to them.
 */
function mimoWorkflowOutcome(status: string): TaskStatus {
  switch (status) {
    case 'failed':
    case 'failure':
      return 'failed'
    case 'cancelled':
      return 'stopped'
    case 'running':
    case 'pending':
      return 'running'
    default:
      return 'completed'
  }
}

/** One kind's declared request: MiMo's own reading, or the shared table's. */
function mimoRequestFor<K extends ToolKind>(kind: K, facts: MiMoToolFacts): ToolRequestByKind[K] {
  return toolRequestFor(kind, facts.part.input, facts, MIMO_TOOL_REQUEST_OVERRIDES)
}

/**
 * The reader of a kind no MiMo tool takes: the declared request, and the words the
 * call printed once it answered.
 */
function mimoArgumentsOnly<P extends ToolKind>(kind: P): (facts: MiMoToolFacts) => ToolCallSpecVariant<P> {
  // The inner arrow states its OWN return type, although the signature above already
  // declares it. A contextual signature is not an annotated position, so without this
  // the literal escapes the excess-property check.
  return (facts): ToolCallSpecVariant<P> => ({ kind, request: mimoRequestFor(kind, facts), ...mimoUnreadResult(facts) })
}

/** The lifecycle of a kind whose answer this build cannot read into a shape. */
function mimoUnreadResult(facts: MiMoToolFacts): { result?: ToolFailureResult | UnparsedToolResult } {
  if (!facts.answered)
    return {}
  if (endedBadly(facts))
    return { result: failedResult(facts.text) }
  return { result: unparsedResult(facts.text) }
}

/**
 * The result side the generic card states: the words the call printed as one text
 * item, then each image the call attached, since the generic kinds carry no typed
 * answer of their own.
 *
 * A Model Context Protocol tool is the usual source of the images. MiMo folds its
 * result into the text output and one file attachment for each image or binary
 * resource (`src/mcp/tool-result.ts`). The output already holds the structured
 * content, serialized, so the card states no second copy of it.
 */
function mimoGenericResult(facts: MiMoToolFacts): { result?: GenericToolResult | ToolFailureResult } {
  if (!facts.answered)
    return {}
  if (endedBadly(facts))
    return { result: failedResult(facts.text) }
  const content: McpContentItem[] = []
  if (facts.text)
    content.push({ type: 'text', text: facts.text })
  for (const source of facts.images)
    content.push({ type: 'image', source })
  return { result: { content } }
}

/** The failure or refusal a call that did not complete states, in the shared shape. */
function failureOf(facts: MiMoToolFacts) {
  return failedResult(facts.text)
}

/** True when the call ended without a plain success. */
function endedBadly(facts: MiMoToolFacts): boolean {
  return facts.failed || facts.declined || facts.aborted
}

/**
 * One reader for each kind, each checked against its OWN kind's request and result.
 *
 * The mapped type requires an entry for every `ToolKind`.
 * A new kind therefore causes a compile error here.
 * Kinds that MiMo does not produce return the declared request and output text.
 * Those entries remain valid if a later release produces such a kind.
 *
 * Every entry declares its return type also.
 * Without the annotation, the object loses freshness before the property check.
 * A specification could then carry an unread key.
 */
export const MIMO_TOOL_READERS: ToolCallSpecReaderTable<MiMoToolFacts> = {
  execute: (facts): ToolCallSpecVariant<'execute'> => {
    const request = mimoRequestFor('execute', facts)
    const title = request.description || undefined
    if (!facts.answered) {
      // A command that the turn cut short printed output until the cut, and its last
      // update is the only record of that output.
      return facts.retained && pickString(facts.part.metadata, 'output')
        ? { kind: 'execute', request, title, result: mimoExecuteResult(facts.part) }
        : { kind: 'execute', request, title }
    }
    if (endedBadly(facts)) {
      // A command that ran and then failed still printed its output. A call that
      // never ran states its reason alone.
      const printed = pickString(facts.part.metadata, 'output')
      return printed
        ? { kind: 'execute', request, title, result: mimoExecuteResult(facts.part), statusOverride: facts.aborted ? 'cancelled' : 'failed' }
        : { kind: 'execute', request, title, result: failureOf(facts) }
    }
    // A script completes its call however it ended, and states the ending apart.
    const scriptOutcome = mimoExecOutcome(facts.part)
    return scriptOutcome
      ? { kind: 'execute', request, title, result: mimoExecuteResult(facts.part), statusOverride: scriptOutcome }
      : { kind: 'execute', request, title, result: mimoExecuteResult(facts.part) }
  },
  read: (facts): ToolCallSpecVariant<'read'> => {
    const request = mimoRequestFor('read', facts)
    // `view_image` spells its file `path`, which the shared entry already reads.
    const images = facts.images.map(image => withFallbackFilePath(image, request.path))
    if (!facts.answered)
      return { kind: 'read', request }
    if (endedBadly(facts))
      return { kind: 'read', request, result: failureOf(facts) }
    const body = facts.read
    if (body?.type === 'file') {
      return {
        kind: 'read',
        request: { ...request, path: request.path || body.path },
        images,
        result: {
          lines: body.lines,
          fallbackContent: body.lines.map(line => line.text).join('\n'),
          ...(body.notice !== undefined ? { trailing: [{ label: 'Range', text: body.notice }, ...body.trailing] } : body.trailing.length > 0 ? { trailing: body.trailing } : {}),
        },
      }
    }
    // The text describes the returned image or PDF. It describes other media also.
    // The row shows each image beside that text.
    return { kind: 'read', request, images, result: unparsedResult(facts.text) }
  },
  list: (facts): ToolCallSpecVariant<'list'> => {
    const request = mimoRequestFor('list', facts)
    const body = facts.read
    if (!facts.answered || body?.type !== 'directory') {
      if (facts.answered && endedBadly(facts))
        return { kind: 'list', request, result: failureOf(facts) }
      return { kind: 'list', request }
    }
    return {
      kind: 'list',
      request: { path: body.path || request.path },
      result: {
        entries: body.entries.map(path => ({ path })),
        ...(body.totalEntries !== undefined ? { totalEntries: body.totalEntries } : {}),
        ...(body.offset !== undefined ? { offset: body.offset } : {}),
        truncated: body.truncated,
      },
    }
  },
  edit: (facts): ToolCallSpecVariant<'edit'> => {
    const request = mimoRequestFor('edit', facts)
    if (!facts.answered)
      return { kind: 'edit', request }
    if (endedBadly(facts))
      return { kind: 'edit', request, result: failureOf(facts) }
    const landed = mimoLandedChanges(facts.part, request.changes[0]?.filePath ?? '')
    if (landed)
      return { kind: 'edit', request, result: { changes: landed } }
    // Nothing states the landed change, so the change the call ASKED for is the best
    // statement of what it applied. It claims nothing beyond the call's own arguments.
    return { kind: 'edit', request, result: { changes: request.changes } }
  },
  write: (facts): ToolCallSpecVariant<'write'> => {
    const request = mimoRequestFor('write', facts)
    if (!facts.answered)
      return { kind: 'write', request }
    if (endedBadly(facts))
      return { kind: 'write', request, result: failureOf(facts) }
    const landed = mimoLandedChanges(facts.part, request.changes[0]?.filePath ?? '')
    return { kind: 'write', request, result: { changes: landed ?? request.changes } }
  },
  glob: (facts): ToolCallSpecVariant<'glob'> => {
    const request = mimoRequestFor('glob', facts)
    if (!facts.answered)
      return { kind: 'glob', request }
    if (endedBadly(facts))
      return { kind: 'glob', request, result: failureOf(facts) }
    const count = pickNumber(facts.part.metadata, 'count', undefined)
    const truncated = pickBoolean(facts.part.metadata, 'truncated') === true
    const listed = count === undefined ? null : mimoGlobFiles(facts.text, count)
    if (!listed)
      return { kind: 'glob', request, result: unparsedResult(facts.text) }
    return {
      kind: 'glob',
      request,
      result: {
        filenames: listed.files,
        content: '',
        numFiles: listed.files.length,
        numLines: 0,
        truncated,
        ...(listed.notice !== undefined ? { notice: listed.notice } : {}),
        fallbackContent: facts.text,
        empty: listed.files.length === 0,
      },
    }
  },
  grep: (facts): ToolCallSpecVariant<'grep'> => {
    const request = mimoRequestFor('grep', facts)
    if (!facts.answered)
      return { kind: 'grep', request }
    if (endedBadly(facts))
      return { kind: 'grep', request, result: failureOf(facts) }
    const matches = pickNumber(facts.part.metadata, 'matches', undefined)
    const truncated = pickBoolean(facts.part.metadata, 'truncated') === true
    const listed = matches === undefined ? null : mimoGrepMatches(facts.text, matches)
    if (!listed)
      return { kind: 'grep', request, result: unparsedResult(facts.text) }
    const lines = listed.matches
    return {
      kind: 'grep',
      request,
      result: {
        filenames: [],
        content: '',
        lines,
        numFiles: new Set(lines.map(line => line.filePath)).size,
        numLines: lines.length,
        ...(matches !== undefined ? { matchCount: matches } : {}),
        truncated,
        ...(listed.notice !== undefined ? { notice: listed.notice } : {}),
        fallbackContent: '',
        empty: lines.length === 0,
      },
    }
  },
  search: (facts): ToolCallSpecVariant<'search'> => {
    const request = mimoRequestFor('search', facts)
    if (!facts.answered)
      return { kind: 'search', request }
    if (endedBadly(facts))
      return { kind: 'search', request, result: failureOf(facts) }
    // These search corpora contain no files:
    // - Code snippets.
    // - Skills.
    // - The tool registry.
    // - History.
    // The result therefore contains the output text.
    return {
      kind: 'search',
      request,
      result: {
        filenames: [],
        content: facts.text,
        numFiles: 0,
        numLines: 0,
        truncated: pickBoolean(facts.part.metadata, 'truncated') === true,
        fallbackContent: facts.text,
        empty: facts.text.trim() === '' || facts.text.trim() === MIMO_SEARCH_EMPTY,
      },
    }
  },
  agent: (facts): ToolCallSpecVariant<'agent'> => {
    const request = mimoRequestFor('agent', facts)
    const title = request.description || undefined
    if (!facts.answered)
      return { kind: 'agent', request, title }
    if (endedBadly(facts))
      return { kind: 'agent', request, title, result: failureOf(facts) }
    const run = mimoActorRun(facts.part)
    if (run)
      return { kind: 'agent', request, title, result: { agents: [{ ...run, ...(facts.registryKey ? { registryKey: facts.registryKey } : {}) }] } }
    return { kind: 'agent', request, title, result: unparsedResult(facts.text) }
  },
  message: (facts): ToolCallSpecVariant<'message'> => {
    const request = mimoRequestFor('message', facts)
    if (!facts.answered)
      return { kind: 'message', request }
    if (endedBadly(facts))
      return { kind: 'message', request, result: failureOf(facts) }
    // MiMo completes a send that reached no subagent, and states the failure in the
    // call's title and its metadata alone.
    const error = pickString(facts.part.metadata, 'error')
    if (error)
      return { kind: 'message', request, result: failedResult(facts.part.title || error), statusOverride: 'failed' }
    return { kind: 'message', request, result: proseResult(facts.text) }
  },
  task: (facts): ToolCallSpecVariant<'task'> => {
    const request = mimoRequestFor('task', facts)
    const title = facts.part.title || undefined
    if (!facts.answered)
      return { kind: 'task', request, title }
    if (endedBadly(facts))
      return { kind: 'task', request, title, result: failureOf(facts) }
    const outcome = facts.part.tool === MIMO_TOOL.Workflow
      ? mimoWorkflowOutcome(pickString(facts.part.metadata, 'status'))
      : mimoActorTaskOutcome(facts.part)
    return { kind: 'task', request, title, result: { outcome, output: facts.text } }
  },
  todo: (facts): ToolCallSpecVariant<'todo'> => {
    const request = mimoRequestFor('todo', facts)
    // MiMo's own words for the call, such as `Task created: T1`. A call that has not
    // answered states none, and the renderer composes the header from the request.
    const title = facts.answered && facts.part.title ? facts.part.title : undefined
    if (!facts.answered)
      return { kind: 'todo', request }
    if (endedBadly(facts))
      return { kind: 'todo', request, title, result: failureOf(facts) }
    const result = mimoTaskResult(facts.part, request)
    return result
      ? { kind: 'todo', request, title, result }
      : { kind: 'todo', request, title, result: unparsedResult(facts.text) }
  },
  question: (facts): ToolCallSpecVariant<'question'> => {
    const request = mimoRequestFor('question', facts)
    const title = request.questions[0]?.header || request.questions[0]?.question || undefined
    if (!facts.answered)
      return { kind: 'question', request, title }
    if (endedBadly(facts))
      return { kind: 'question', request, title, result: failureOf(facts) }
    const answers = Array.isArray(facts.part.metadata.answers) ? facts.part.metadata.answers : null
    if (!answers)
      return { kind: 'question', request, title, result: unparsedResult(facts.text) }
    return {
      kind: 'question',
      request,
      title,
      result: {
        answers: request.questions.map((question, index) => {
          const chosen = answers[index]
          const words = Array.isArray(chosen) ? chosen.filter((word): word is string => typeof word === 'string' && word !== '') : []
          return { header: question.header || question.question, answer: words.length > 0 ? words.join(', ') : null }
        }),
      },
    }
  },
  switch_mode: (facts): ToolCallSpecVariant<'switch_mode'> => {
    const request = mimoRequestFor('switch_mode', facts)
    const title = facts.part.title || undefined
    if (!facts.answered)
      return { kind: 'switch_mode', request }
    if (facts.failed || facts.aborted)
      return { kind: 'switch_mode', request, title, result: failureOf(facts) }
    if (facts.declined)
      return { kind: 'switch_mode', request, title, result: failureOf(facts), statusOverride: 'declined' }
    // A call outside plan mode asked nobody, so nothing was approved or sent back.
    if (mimoPlanExitOutsidePlanMode(facts))
      return { kind: 'switch_mode', request, title, result: proseResult(facts.text) }
    // Sending a plan back answers the call.
    // The session stays in plan mode and retains the user's feedback.
    if (pickBoolean(facts.part.metadata, 'switched') === false && facts.part.metadata.feedback !== undefined) {
      const feedback = pickString(facts.part.metadata, 'feedback')
      return { kind: 'switch_mode', request, title, result: proseResult(feedback || facts.text), statusOverride: 'declined' }
    }
    return { kind: 'switch_mode', request, title, result: proseResult(facts.text) }
  },
  fetch: (facts): ToolCallSpecVariant<'fetch'> => {
    const request = mimoRequestFor('fetch', facts)
    if (!facts.answered)
      return { kind: 'fetch', request }
    if (endedBadly(facts))
      return { kind: 'fetch', request, result: failureOf(facts) }
    return { kind: 'fetch', request, result: { result: facts.text } }
  },
  web_search: (facts): ToolCallSpecVariant<'web_search'> => {
    const request = mimoRequestFor('web_search', facts)
    if (!facts.answered)
      return { kind: 'web_search', request }
    if (endedBadly(facts))
      return { kind: 'web_search', request, result: failureOf(facts) }
    return { kind: 'web_search', request, result: { links: [], summary: facts.text } }
  },
  skill: (facts): ToolCallSpecVariant<'skill'> => {
    const request = mimoRequestFor('skill', facts)
    if (!facts.answered)
      return { kind: 'skill', request }
    if (endedBadly(facts))
      return { kind: 'skill', request, result: failureOf(facts) }
    return { kind: 'skill', request, result: proseResult(facts.text, 'markdown') }
  },
  memory: (facts): ToolCallSpecVariant<'memory'> => {
    const request = mimoRequestFor('memory', facts)
    if (!facts.answered)
      return { kind: 'memory', request }
    if (endedBadly(facts))
      return { kind: 'memory', request, result: failureOf(facts) }
    return { kind: 'memory', request, result: proseResult(facts.text) }
  },
  trigger: (facts): ToolCallSpecVariant<'trigger'> => {
    const request = mimoRequestFor('trigger', facts)
    const title = facts.part.title || undefined
    if (!facts.answered)
      return { kind: 'trigger', request, title }
    if (endedBadly(facts))
      return { kind: 'trigger', request, title, result: failureOf(facts) }
    return { kind: 'trigger', request, title, result: proseResult(facts.text) }
  },
  agents: (facts): ToolCallSpecVariant<'agents'> => {
    const request = mimoRequestFor('agents', facts)
    const title = facts.part.title || undefined
    if (!facts.answered)
      return { kind: 'agents', request, title }
    if (endedBadly(facts))
      return { kind: 'agents', request, title, result: failureOf(facts) }
    return { kind: 'agents', request, title, result: proseResult(facts.text) }
  },
  // The generic card: `invalid`, and a tool from a later MiMo release.
  other: (facts): ToolCallSpecVariant<'other'> => ({ kind: 'other', request: mimoRequestFor('other', facts), ...mimoGenericResult(facts) }),
  // A stored row always identifies its tool, so it cannot use this kind.
  // The complete table still supplies the same card for this entry.
  unspecified: (facts): ToolCallSpecVariant<'unspecified'> => ({ kind: 'unspecified', request: mimoRequestFor('unspecified', facts), ...mimoGenericResult(facts) }),
  // No MiMo tool uses this kind.
  // MiMo identifies an MCP tool as `<server>_<tool>` without separate server and tool fields.
  // That tool therefore uses `other`. The complete table still supplies the same card here.
  mcp: (facts): ToolCallSpecVariant<'mcp'> => ({ kind: 'mcp', request: mimoRequestFor('mcp', facts), ...mimoGenericResult(facts) }),
  chart: mimoArgumentsOnly('chart'),
  delete: mimoArgumentsOnly('delete'),
  image: mimoArgumentsOnly('image'),
  move: mimoArgumentsOnly('move'),
  report: mimoArgumentsOnly('report'),
  think: mimoArgumentsOnly('think'),
  wait: mimoArgumentsOnly('wait'),
}

/** The specification of one call, read from the facts. The table covers `ToolKind`. */
function mimoSpecFor(facts: MiMoToolFacts): ToolCallSpecVariant<ToolKind> {
  return readToolCallSpec(MIMO_TOOL_READERS, facts.kind, facts)
}
