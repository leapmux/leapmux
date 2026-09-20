import type { FileEditDiff } from '../../../model/fileEditDiff'
import type { ProseResult, ToolCall, ToolCallEnvelope, ToolCallSpecReaderTable, ToolCallSpecVariant, ToolFailureResult, UnparsedToolResult } from '../../../model/toolCall'
import type { ToolKind } from '../../../model/toolKind'
import type { ToolRequestByKind } from '../../../model/tools'
import type { FileChangeResult } from '../../../model/tools/fileChange'
import type { GenericToolResult } from '../../../model/tools/generic'
import type { SearchResult } from '../../../model/tools/search'
import type { TaskStatus } from '../../../model/tools/task'
import type { TriggerRequest } from '../../../model/tools/trigger'
import type { ToolRequestOverrides } from '../../defaultToolRequests'
import type { ZCodeResultDisplay } from '../extractors/display'
import type { ZCodeRow, ZCodeToolUpdate } from '../extractors/toolCommon'
import type { ImageResultSource } from '~/lib/imageBlocks'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { ZCODE_TOOL, ZCODE_TOOL_KIND } from '~/generated/contracts/zcode-protocol'
import { prettifyArgsJson } from '~/lib/jsonFormat'
import { isObject, pickFirstString, pickObject, pickString } from '~/lib/jsonPick'
import { applyPatchFileChanges } from '../../../model/applyPatch'
import { createToolCall } from '../../../model/createToolCall'
import { parseMcpToolName } from '../../../model/mcpToolCall'
import { failedResult, proseResult, readToolCallSpec, unparsedResult } from '../../../model/toolCall'
import { DEFAULT_TOOL_REQUESTS, toolRequestFor } from '../../defaultToolRequests'
import { retainedOutcome } from '../../registry'
import { TOOL_FILE_PATH_KEYS } from '../../toolInputKeys'
import { zcodeQuestionsFromToolInput } from '../askUserQuestion'
import { zcodeAgentResult } from '../extractors/agent'
import { zcodeResultDisplay } from '../extractors/display'
import { extractZCodeBash, zcodeBashToCommandResult } from '../extractors/execute'
import { extractZCodeFileDiff, extractZCodeRead, zcodeFilePath } from '../extractors/fileEdit'
import { zcodeToolResultImages } from '../extractors/image'
import { extractZCodeSearch } from '../extractors/search'
import { zcodeErrorText, zcodeExtractTool, zcodeTodoItemsFromInput, zcodeToolInput, zcodeToolSpanRole } from '../extractors/toolCommon'
import { ZCODE_DISPLAY } from '../protocol'
import { zcodeToolKind } from '../toolKinds'

/** The tools whose input states a file the row titles itself with. */
const ZCODE_FILE_KINDS: ReadonlySet<ToolKind> = new Set<ToolKind>(['read', 'write', 'edit'])

/** The three tools that run code in ZCode's own JavaScript sandbox. */
const ZCODE_SANDBOX_TOOLS: ReadonlySet<string> = new Set<string>([ZCODE_TOOL.Js, ZCODE_TOOL.JsAddNodeModuleDir, ZCODE_TOOL.JsReset])

/**
 * The action each ZCode cron tool performs.
 *
 * The tool NAME is where ZCode states it: one tool for each half of the lifecycle,
 * and no `action` argument beside it. The arguments still answer first, because a
 * later release may add one, and this table is the answer when they state nothing.
 */
const ZCODE_TRIGGER_ACTIONS: ReadonlyMap<string, TriggerRequest['action']> = new Map<string, TriggerRequest['action']>([
  [ZCODE_TOOL.CronCreate, 'create'],
  [ZCODE_TOOL.CronList, 'list'],
  [ZCODE_TOOL.CronUpdate, 'update'],
  [ZCODE_TOOL.CronDelete, 'delete'],
])

/**
 * The kinds whose row states NO truncation flag, for two different reasons.
 *
 * The command body and the search body each draw a notice of their own, so a second
 * one would print `Output truncated` twice under one result. The rich-content card
 * replaces the whole row and draws no part of the presentation around it, so a flag
 * there reaches nothing at all.
 */
const ZCODE_ROW_STATES_NO_TRUNCATION: ReadonlySet<ToolKind> = new Set<ToolKind>(['execute', 'glob', 'grep'])

/**
 * Everything one ZCode row states, collected once before any specification decision runs.
 *
 * The specification build used to take seven positional parameters and re-enter the row
 * readers from inside its branches: `zcodeDisplayHint` ran four times, the result
 * display twice, the tool input three times, and `update.isError` was unpacked again
 * under a second name. Each of those is one fact, so each is one field here.
 *
 * Membership is what the DECISIONS read, not a copy of another provider's struct. A
 * fact that exactly one kind reads stays in that kind's reader -- the Bash command,
 * the read body, the search hits and the subagent record are all read once. The file
 * diff is here because two decisions read it: the `edit` and `write` request states
 * the change that landed, and their result states it again. The patch changes are here
 * for the same reason, and the parse is a walk of every line of a whole patch text.
 */
export interface ZCodeToolFacts {
  /** The row itself, for the per-kind extractors that read the whole frame. */
  row: ZCodeRow
  /** The `tool.updated` payload, normalized across its six kinds. */
  update: ZCodeToolUpdate
  /** The resolved tool name. `''` when no source states one. */
  toolName: string
  /** The kind this row takes, after {@link zcodeReclassify}. */
  kind: ToolKind
  /** The tool arguments, with a file tool's path recovered from the result display. */
  input: Record<string, unknown>
  /** The raw `display` object of the result, or undefined. */
  rawDisplay: Record<string, unknown> | undefined
  /** The raw `display.kind` word the app-server sent, before the shared shapes fold it. */
  displayHint: string | undefined
  /** The result display in the shared shapes, or null where the hint states none. */
  display: ZCodeResultDisplay | null
  /** The diff the result carries, or the one the arguments ask for. Null for neither. */
  fileDiff: FileEditDiff | null
  /**
   * One change for each file of an `ApplyPatch` envelope.
   *
   * Null for every other tool, and for an envelope the shared reader refuses. Only
   * `ApplyPatch` sends this text: a `patch` argument on any other tool is that tool's
   * own, and reading it as an apply-patch envelope would put another file in the row.
   */
  patchChanges: FileEditDiff[] | null
  /** The words this row states: the failure text, the result content, or the progress tails. */
  text: string
  /** True when this row is the last one of its tool call. */
  finished: boolean
  /** True when this event supplies a result or an error. */
  resultFrameLanded: boolean
  /** Whether this row can build a result, including a retained progress body. */
  resultAvailable: boolean
  /** True when the call reported an error. */
  failed: boolean
  /** The provider kept only part of what the call produced. */
  truncated: boolean
  /** Every picture the result display carries. */
  images: ImageResultSource[]
  /**
   * The pictures of a NODE-IMAGE display alone.
   *
   * Separate from {@link ZCodeToolFacts.images} because the two readings differ: a read
   * row takes whatever pictures the display holds, and an execute or a generic row takes
   * them only when the sandbox drew them.
   */
  nodeImages: ImageResultSource[]
}

/**
 * One ZCode tool call, as the kind-discriminated pair. Null for a row that is not one.
 */
export function zcodeToolCall(row: ZCodeRow, parsed?: ParsedMessageContent): ToolCall | null {
  const update = zcodeExtractTool(row.parsed)
  if (!update)
    return null
  const facts = zcodeToolFacts(row, update, parsed)
  const pairedResult = zcodeExtractTool(row.result?.parentObject)
  const resultFrameLanded = update.kind === ZCODE_TOOL_KIND.Result
    || update.kind === ZCODE_TOOL_KIND.Error
    || update.kind === ZCODE_TOOL_KIND.Batch
    || pairedResult?.kind === ZCODE_TOOL_KIND.Result
    || pairedResult?.kind === ZCODE_TOOL_KIND.Error
    || pairedResult?.kind === ZCODE_TOOL_KIND.Batch
  const envelope: ToolCallEnvelope = {
    id: update.toolCallId,
    name: row.toolName,
    lifecycle: {
      frameStatus: 'unstated',
      providerOutcome: update.isError ? 'failed' : null,
      retainedOutcome: retainedOutcome(parsed?.completion),
      rowFinal: facts.finished,
      resultFrameLanded,
    },
  }
  const spec = zcodeSpecFor(facts, facts.kind)
  // The provider CUT the content. Every body outside the set below states nothing
  // about that, so the row's own flag is the only notice a truncated read, fetch,
  // agent, to-do, task or message row can get -- each of them used to end mid-content
  // with no word for why.
  const marked = facts.truncated && !ZCODE_ROW_STATES_NO_TRUNCATION.has(spec.kind) ? { ...spec, truncated: true } : spec
  // The tool's OWN name, which the icon tooltip states. A row that states none
  // falls back to the kind's word, because a name invented here is not one the
  // agent reported.
  const label = marked.label ?? (row.toolName || undefined)
  return createToolCall(envelope, { ...marked, ...(label !== undefined ? { label } : {}) })
}

/**
 * Collect every fact one row states, and settle the kind it takes.
 *
 * THREE steps, in this order. {@link zcodeClassifyTool} reads the display hint and the
 * name table, {@link zcodeResolvedInput} then recovers a file path the arguments left
 * out, and {@link zcodeReclassify} applies the swaps last. The recovery runs on the
 * FIRST kind, because it asks whether that kind is a file kind; the swaps run after
 * it, because one of them asks whether a file change states a file.
 */
export function zcodeToolFacts(row: ZCodeRow, update: ZCodeToolUpdate, parsed: ParsedMessageContent | undefined): ZCodeToolFacts {
  const rawDisplay = update.result?.display ?? undefined
  const displayHint = pickString(rawDisplay, 'kind') || undefined
  // A PROGRESS frame states its partial output as two tails on the payload, which
  // the shared result shape reads as the content.
  const payloadOf = isObject(row.parsed) ? pickObject(row.parsed, 'payload') : undefined
  const progressText = update.kind === ZCODE_TOOL_KIND.Progress
    ? [pickString(payloadOf, 'stdoutTail'), pickString(payloadOf, 'stderrTail')].filter(Boolean).join('\n')
    : ''
  const images = zcodeToolResultImages(row)
  const input = zcodeToolInput(row)
  const finished = zcodeToolSpanRole(update.kind, parsed) === 'result'
  const resultFrameLanded = update.kind === ZCODE_TOOL_KIND.Result
    || update.kind === ZCODE_TOOL_KIND.Error
    || update.kind === ZCODE_TOOL_KIND.Batch
  const pairedResult = zcodeExtractTool(row.result?.parentObject)
  const pairedResultFrameLanded = pairedResult?.kind === ZCODE_TOOL_KIND.Result
    || pairedResult?.kind === ZCODE_TOOL_KIND.Error
    || pairedResult?.kind === ZCODE_TOOL_KIND.Batch
  const classified: ZCodeToolFacts = {
    row,
    update,
    toolName: row.toolName,
    kind: zcodeClassifyTool(row, displayHint),
    // The RAW arguments. The file-path recovery below replaces this field, and
    // `zcodeReclassify` reads the recovered copy.
    input,
    rawDisplay,
    displayHint,
    display: zcodeResultDisplay(row),
    fileDiff: extractZCodeFileDiff(row),
    // The RAW arguments answer here too. `zcodeResolvedInput` adds a file path and
    // takes nothing away, so the `patch` it would hand over is this same text.
    patchChanges: row.toolName === ZCODE_TOOL.ApplyPatch ? applyPatchFileChanges(pickString(input, 'patch')) : null,
    text: update.isError ? zcodeErrorText(update) || 'Tool call failed' : (update.result?.content ?? progressText),
    finished,
    resultFrameLanded,
    resultAvailable: resultFrameLanded || pairedResultFrameLanded || (finished && update.result !== null),
    failed: update.isError,
    truncated: rawDisplay?.truncated === true || update.result?.truncated === true,
    images,
    nodeImages: displayHint === ZCODE_DISPLAY.NodeImages ? images : [],
  }
  // No swap moves a call INTO a file kind, so running the recovery on the first kind
  // recovers the same path the final kind would. The file-change swap needs this
  // order: a write whose path arrives only in the result display states its file
  // after the recovery, never before it.
  const resolved = { ...classified, input: zcodeResolvedInput(classified.kind, row, input) }
  return { ...resolved, kind: zcodeReclassify(resolved) }
}

/**
 * The kind one call takes: the display hint first, then the name table.
 *
 * An explicit `file_diff` display wins over the name, because the app-server rendered
 * the result as a diff whatever tool it identifies -- including none.
 */
function zcodeClassifyTool(row: ZCodeRow, displayHint: string | undefined): ToolKind {
  if (displayHint === ZCODE_DISPLAY.FileDiff)
    return 'edit'
  if (displayHint === ZCODE_DISPLAY.McpTool || displayHint === ZCODE_DISPLAY.ComputerUse)
    return 'mcp'
  if (displayHint === ZCODE_DISPLAY.TaskOutput || displayHint === ZCODE_DISPLAY.TaskStop)
    return 'task'
  if (displayHint === ZCODE_DISPLAY.LocalAgentMessage || displayHint === ZCODE_DISPLAY.RespondToCoordinator)
    return 'message'
  if (parseMcpToolName(row.toolName))
    return 'mcp'
  // The table states the kind the row TAKES. It used to answer `search` and `think`
  // for two tools and the caller rewrote both two lines later, so a reader of the
  // table saw a kind no row carries -- and a future ZCode tool legitimately mapped to
  // `search` (a query against a corpus the session holds) or `think` (a real
  // reasoning step) would have been
  // rewritten by a rule meant for those two.
  return zcodeToolKind(row.toolName)
}

/**
 * The kind a row takes AFTER its first classification, as one named step.
 *
 * Three swaps, and each one states the input that causes it. They live here rather than
 * inside the specification build for the reason the comment above gives: a build that names
 * one kind and rewrites it two lines later hides the real answer from every reader of
 * the table, and it is what let a kind be named while its request went unfilled.
 *
 * No swap is dead. `createToolCall.test.ts` reaches all three: a `TodoWrite` whose input
 * carries no list, a result row that states no tool name at all, and an `ApplyPatch`
 * whose envelope the shared reader refuses.
 */
export function zcodeReclassify(facts: ZCodeToolFacts): ToolKind {
  // A to-do tool whose input carries no `todos` array states no list at all, so the
  // row takes the generic card rather than an empty checklist.
  if (facts.kind === 'todo' && zcodeTodoItemsFromInput(facts.input) === null)
    return 'other'
  // A row whose tool NAME is empty takes the same generic card. The two kinds draw the
  // same body, and the label the envelope states is the only thing that separates
  // them -- which an empty name cannot supply either.
  if (facts.kind === 'unspecified')
    return 'other'
  // A file change that names NO file is not a file change. The model refuses the pair --
  // the row composes its header from that list at every state of the call -- and
  // degrades such a call itself, to a row whose arguments are the empty change list.
  // Degrading here instead keeps the arguments the tool sent, which for an
  // `ApplyPatch` this build cannot read is the patch text itself: the one record of
  // what the call asked for.
  if ((facts.kind === 'edit' || facts.kind === 'write') && zcodeFileChanges(facts.kind, facts.input, facts).length === 0)
    return 'other'
  return facts.kind
}

/**
 * The tool INPUT of one row, with the file path recovered from the result display.
 *
 * A ZCode file tool that omits its path from the arguments still states it in the
 * display hint of its result, and the shared title reads the input alone.
 */
function zcodeResolvedInput(kind: ToolKind, row: ZCodeRow, input: Record<string, unknown>): Record<string, unknown> {
  if (!ZCODE_FILE_KINDS.has(kind) || pickFirstString(input, TOOL_FILE_PATH_KEYS))
    return input
  const filePath = zcodeFilePath(row)
  return filePath ? { ...input, filePath } : input
}

/**
 * The kinds ZCode reads DIFFERENTLY from the shared table, and nothing else.
 *
 * Ten entries, and each one reads a fact the arguments alone cannot supply: the result
 * display, the tool name, the parsed questions, the landed diff. Every other kind ZCode
 * produces -- `read`, `glob`, `grep`, `fetch`, `web_search`, `switch_mode`, `skill`,
 * `other` -- takes `DEFAULT_TOOL_REQUESTS`, which reads the same argument keys every
 * provider reads.
 *
 * PARTIAL by its type, and the key set IS the deviation list. No type can refuse a
 * spread of the shared table here, because an entry that takes `args` alone satisfies a
 * slot that supplies `args` and the facts -- so `createToolCall.test.ts` pins these keys and
 * the argument keys each one reads.
 *
 * EVERY entry declares its own return type, and the annotation is load-bearing rather
 * than decorative. The contextual signature this mapped type supplies is NOT an
 * annotated position: TypeScript infers an un-annotated arrow's return type from the
 * literals it returns, so the object loses its freshness before any property is checked
 * and the excess-property check never runs. `todo: args => ({ items, patchText })`
 * compiles with the undeclared key. `todo: (args): ToolRequestByKind['todo'] =>` rejects it.
 */
export const ZCODE_TOOL_REQUEST_OVERRIDES: ToolRequestOverrides<ZCodeToolFacts> = {
  // The subagent TYPE, which ZCode spells `subagent_type`. The shared entry declares no
  // such field, and it reads `instructions` beside `prompt`, which ZCode never sends.
  agent: (args): ToolRequestByKind['agent'] => ({ description: pickString(args, 'description'), agentType: pickString(args, 'subagent_type'), prompt: pickString(args, 'prompt') }),
  // The change that LANDED, which the result display carries as a structured patch.
  // The shared entry states an empty list, because no other provider's arguments
  // describe a diff.
  edit: (args, facts): ToolRequestByKind['edit'] => ({ changes: zcodeFileChanges('edit', args, facts), ...(args.replace_all === true ? { replaceAll: true } : {}) }),
  write: (args, facts): ToolRequestByKind['write'] => ({ changes: zcodeFileChanges('write', args, facts), ...(args.replace_all === true ? { replaceAll: true } : {}) }),
  // The LANGUAGE, which the tool name states: the three JavaScript tools run code in
  // ZCode's own sandbox. The shared entry sees the arguments alone.
  execute: (args, facts): ToolRequestByKind['execute'] => {
    const description = pickString(args, 'description')
    return {
      command: facts.toolName === ZCODE_TOOL.EvalWorkflowSnippet
        ? pickString(args, 'code') || pickString(args, 'path')
        : pickString(args, 'command'),
      ...(ZCODE_SANDBOX_TOOLS.has(facts.toolName) ? { language: 'javascript' } : {}),
      ...(description ? { description } : {}),
    }
  },
  // The SERVER and the TOOL, which the result display's own source states and the wire
  // name states after it. The shared entry reads two arguments no ZCode call carries.
  mcp: (args, facts): ToolRequestByKind['mcp'] => {
    const identity = facts.display?.kind === 'mcp'
      ? { server: facts.display.source.server, tool: facts.display.source.tool }
      : parseMcpToolName(facts.toolName)
    // The display's own `input` string states the arguments: pretty-print it the
    // shared way, which keeps a big integer the app-server sent exactly.
    const displayInput = facts.display?.kind === 'mcp' ? pickString(facts.rawDisplay, 'input') : ''
    return {
      server: identity?.server ?? '',
      tool: identity?.tool ?? facts.toolName,
      args,
      ...(displayInput ? { argsText: prettifyArgsJson(displayInput) } : {}),
    }
  },
  // The words the RESULT carries, which a message states when its arguments do not,
  // and ZCode's own `recipient` spelling. The shared entry reads neither.
  message: (args, facts): ToolRequestByKind['message'] => {
    const to = pickString(args, 'to') || pickString(args, 'recipient')
    return {
      ...(to ? { to } : {}),
      text: pickString(args, 'message') || facts.text,
    }
  },
  // The parsed QUESTIONS. The shared entry states an empty list, because the shape of
  // a question is each provider's own.
  question: (args): ToolRequestByKind['question'] => ({ questions: zcodeQuestionsFromToolInput(args) }),
  // The ACTION, which the display hint states: one hint reads a task's output and the
  // other stops it. The shared entry answers `other` for every call.
  task: (args, facts): ToolRequestByKind['task'] => {
    const taskId = pickString(args, 'task_id') || pickString(args, 'taskId')
      || pickString(args, 'run_id') || pickString(args, 'question_id') || pickString(args, 'name')
    const listsWorkflows = facts.toolName === ZCODE_TOOL.ListSavedWorkflows
      || facts.toolName === ZCODE_TOOL.ListModels
      || facts.toolName === ZCODE_TOOL.ListWorkflowRuns
    return {
      action: facts.displayHint === ZCODE_DISPLAY.TaskStop
        ? 'stop'
        : facts.displayHint === ZCODE_DISPLAY.TaskOutput
          ? 'output'
          : listsWorkflows ? 'list' : 'other',
      ...(taskId ? { taskId } : {}),
    }
  },
  // The parsed ITEMS. The shared entry states an empty list for the same reason the
  // question entry does.
  todo: (args): ToolRequestByKind['todo'] => ({ items: zcodeTodoItemsFromInput(args) ?? [] }),
  // The ACTION alone, which the TOOL NAME states: one cron tool for each half of the
  // lifecycle, and no `action` argument beside it. The shared entry answers `other` for
  // every call, because no other provider's name reaches it.
  //
  // The id, the label and the schedule come from that shared entry and are read here no
  // longer. ZCode spells all three the way every provider does, so a reading of its own
  // was a second copy of a neutral one -- the rule that `ToolRequestOverrides` states.
  trigger: (args, facts): ToolRequestByKind['trigger'] => ({
    ...DEFAULT_TOOL_REQUESTS.trigger(args),
    action: zcodeTriggerAction(args, facts.toolName),
  }),
}

/** One kind's declared request: ZCode's own reading, or the shared table's. */
function zcodeRequestFor<K extends ToolKind>(kind: K, facts: ZCodeToolFacts): ToolRequestByKind[K] {
  return toolRequestFor(kind, facts.input, facts, ZCODE_TOOL_REQUEST_OVERRIDES)
}

/**
 * One reader for each kind, each checked against its OWN kind's request and result.
 *
 * A `switch` over the kind cannot do this. TypeScript narrows the VALUE the switch
 * tests and never the type parameter, so a branch that filled another kind's shape --
 * or filled none at all -- compiled behind a declared specification type. That is the defect
 * class this table removes: a kind cannot be named here without its declared request
 * beside it, because the entry's own value type states the pair.
 *
 * Total over `ToolKind` by the mapped type, so a new kind is a compile error here.
 * ZCode produces eighteen of the thirty. The rest are {@link zcodeArgumentsOnly}: they
 * answer the shared request and the words the call printed, which is the right answer
 * if a later ZCode release ever reaches one.
 *
 * EVERY entry declares its own return type, and that annotation is the SECOND half of
 * the check. The mapped type states which kind each entry answers for; it does not put
 * the entry's literal under the excess-property check. TypeScript infers an un-annotated
 * arrow's return type from the literals it returns, so the object loses its freshness
 * before any property is checked, and a specification can then carry an unread key.
 * For the same reason, no entry may return an intermediate `const` that declares no type
 * of its own: a variable reference is not a fresh literal either.
 * `toolTableEntriesAreAnnotated.test.ts` keeps both halves in place.
 */
export const ZCODE_TOOL_READERS: ToolCallSpecReaderTable<ZCodeToolFacts> = {
  mcp: (facts): ToolCallSpecVariant<'mcp'> => {
    const request = zcodeRequestFor('mcp', facts)
    if (!facts.resultAvailable)
      return { kind: 'mcp', request }
    const source = facts.display?.kind === 'mcp' ? facts.display.source : null
    if (source) {
      return {
        kind: 'mcp',
        request,
        result: {
          content: source.content,
          ...(source.structuredJson !== undefined ? { structuredJson: source.structuredJson } : {}),
          ...(source.error !== undefined ? { error: source.error } : {}),
          ...(source.durationMs !== undefined ? { durationMs: source.durationMs } : {}),
        },
        ...(source.failed && !facts.failed ? { statusOverride: 'failed' as const } : {}),
      }
    }
    if (facts.failed)
      return { kind: 'mcp', request, result: failedResult(facts.text) }
    return { kind: 'mcp', request, result: unparsedResult(facts.text) }
  },
  task: (facts): ToolCallSpecVariant<'task'> => {
    const request = zcodeRequestFor('task', facts)
    const title = zcodeCallTitle(facts)
    if (!facts.resultAvailable)
      return { kind: 'task', request, title }
    const display = facts.display?.kind === 'status' ? facts.display : null
    if (display) {
      return {
        kind: 'task',
        request,
        title,
        result: { title: display.title, outcome: zcodeTaskStatus(display.status), ...(display.command !== undefined ? { command: display.command } : {}), output: display.output },
      }
    }
    if (facts.failed)
      return { kind: 'task', request, title, result: failedResult(facts.text) }
    return { kind: 'task', request, title, result: { outcome: 'completed', output: facts.text } }
  },
  message: (facts): ToolCallSpecVariant<'message'> => {
    const request = zcodeRequestFor('message', facts)
    const title = zcodeCallTitle(facts)
    const display = facts.display?.kind === 'status' ? facts.display : null
    // A message display states its own words -- 'Message sent', 'Failed' -- which
    // the row keeps as its answer, and its failure folds into the outcome word.
    const statesMessage = facts.displayHint === ZCODE_DISPLAY.LocalAgentMessage || facts.displayHint === ZCODE_DISPLAY.RespondToCoordinator
    const words = display && statesMessage ? [display.title, display.output].filter(Boolean).join('\n') : ''
    if (!facts.resultAvailable)
      return { kind: 'message', request, title }
    if (facts.failed || display?.status === 'failed')
      return { kind: 'message', request, title, statusOverride: 'failed', result: failedResult(words || facts.text) }
    return { kind: 'message', request, title, result: proseResult(words || facts.text) }
  },
  agent: (facts): ToolCallSpecVariant<'agent'> => {
    const request = zcodeRequestFor('agent', facts)
    const title = zcodeCallTitle(facts)
    if (!facts.resultAvailable)
      return { kind: 'agent', request, title }
    const source = zcodeAgentResult(facts.row)
    if (source)
      return { kind: 'agent', request, title, result: { agents: [source] } }
    return { kind: 'agent', request, title, ...(facts.text ? { result: unparsedResult(facts.text) } : {}) }
  },
  todo: (facts): ToolCallSpecVariant<'todo'> => {
    // NEVER empty by accident here: `zcodeReclassify` answers `other` for an input
    // that carries no todos array, so a row that reaches this reader states a list.
    const request = zcodeRequestFor('todo', facts)
    // No title: `todoRenderer` composes the same words from the request this payload
    // carries, and a copy here is a second place for the wording to drift.
    if (!facts.resultAvailable)
      return { kind: 'todo', request }
    if (facts.failed)
      return { kind: 'todo', request, result: failedResult(facts.text) }
    return { kind: 'todo', request, result: { items: request.items } }
  },
  question: (facts): ToolCallSpecVariant<'question'> => {
    const request = zcodeRequestFor('question', facts)
    const title = zcodeCallTitle(facts)
    if (!facts.resultAvailable)
      return { kind: 'question', request, title }
    if (facts.failed)
      return { kind: 'question', request, title, result: failedResult(facts.text) }
    // The QUESTION's own header, not the row title -- which for a non-execute kind
    // falls back to the tool name, so a finished question read
    // "**AskUserQuestion** - <the chosen option>".
    const header = request.questions[0]?.header || request.questions[0]?.question || 'Question'
    return { kind: 'question', request, title, ...(facts.text ? { result: { answers: [{ header, answer: facts.text }] } } : {}) }
  },
  execute: (facts): ToolCallSpecVariant<'execute'> => {
    // NO title. The command states itself in the shared header, and every other kind's
    // title falls back to the tool name -- which would sit above the command it ran.
    const request = zcodeRequestFor('execute', facts)
    // A call that has not ended carries NO result, whatever a PROGRESS frame's output
    // tails hold. `ToolMessage` draws the live output the worker broadcasts only while
    // the row states no result, so the tails here replaced a stream that is ahead of
    // them with a card that never grows -- and the model refuses the pair outright, which
    // sent the whole row to the uncategorized card. A RETAINED progress frame is
    // finished, and its tails reach the row below as the words the call printed.
    if (!facts.resultAvailable)
      return { kind: 'execute', request }
    const command = extractZCodeBash(facts.row)
    if (command) {
      // The command body draws its own notice, so the row keeps no flag -- a second
      // one would print `Output truncated` twice under one result.
      const source = facts.truncated ? { ...zcodeBashToCommandResult(command), truncated: true } : zcodeBashToCommandResult(command)
      // A timed-out Bash says the call was STOPPED, not failed, and the command body
      // reads that label off the one outcome word. `statusOverride` is the declared
      // way for a payload to state an outcome its envelope cannot see -- the envelope
      // used to parse the whole Bash result a second time to ask this one question.
      return {
        kind: 'execute',
        request,
        result: { commands: [source], unresolvedTerminals: [] },
        images: facts.nodeImages,
        ...(command.timedOut ? { statusOverride: 'cancelled' } : {}),
      }
    }
    if (facts.failed)
      return { kind: 'execute', request, result: failedResult(facts.text), images: facts.nodeImages }
    return { kind: 'execute', request, result: unparsedResult(facts.text), images: facts.nodeImages }
  },
  read: (facts): ToolCallSpecVariant<'read'> => {
    const request = zcodeRequestFor('read', facts)
    if (!facts.resultAvailable)
      return { kind: 'read', request }
    if (facts.failed)
      return { kind: 'read', request, result: failedResult(facts.text) }
    const source = extractZCodeRead(facts.row)
    if (source)
      return { kind: 'read', request, result: source, images: facts.images }
    return { kind: 'read', request, result: unparsedResult(facts.text), images: facts.images }
  },
  // Two entries for one reading, because each states its OWN kind. A shared generic
  // entry would put the kind and the request beyond the checker again, which is the
  // defect this table exists to remove.
  glob: (facts): ToolCallSpecVariant<'glob'> => ({ kind: 'glob', request: zcodeRequestFor('glob', facts), ...zcodeSearchResult(facts) }),
  grep: (facts): ToolCallSpecVariant<'grep'> => ({ kind: 'grep', request: zcodeRequestFor('grep', facts), ...zcodeSearchResult(facts) }),
  edit: (facts): ToolCallSpecVariant<'edit'> => ({ kind: 'edit', request: zcodeRequestFor('edit', facts), ...zcodeFileChangeResult(facts) }),
  write: (facts): ToolCallSpecVariant<'write'> => ({ kind: 'write', request: zcodeRequestFor('write', facts), ...zcodeFileChangeResult(facts) }),
  fetch: (facts): ToolCallSpecVariant<'fetch'> => {
    const request = zcodeRequestFor('fetch', facts)
    if (!facts.resultAvailable)
      return { kind: 'fetch', request }
    if (facts.failed)
      return { kind: 'fetch', request, result: failedResult(facts.text) }
    return { kind: 'fetch', request, result: { result: facts.text, ...(facts.update.durationMs != null ? { durationMs: facts.update.durationMs } : {}) } }
  },
  web_search: (facts): ToolCallSpecVariant<'web_search'> => {
    const request = zcodeRequestFor('web_search', facts)
    if (!facts.resultAvailable)
      return { kind: 'web_search', request }
    if (facts.failed)
      return { kind: 'web_search', request, result: failedResult(facts.text) }
    return { kind: 'web_search', request, result: { links: [], summary: facts.text } }
  },
  // The three kinds whose declared result IS prose. Each states its own kind for the
  // reason the search pair states theirs.
  switch_mode: (facts): ToolCallSpecVariant<'switch_mode'> => ({ kind: 'switch_mode', request: zcodeRequestFor('switch_mode', facts), title: zcodeCallTitle(facts), ...zcodeProseResult(facts) }),
  skill: (facts): ToolCallSpecVariant<'skill'> => ({ kind: 'skill', request: zcodeRequestFor('skill', facts), title: zcodeCallTitle(facts), ...zcodeProseResult(facts) }),
  trigger: (facts): ToolCallSpecVariant<'trigger'> => ({ kind: 'trigger', request: zcodeRequestFor('trigger', facts), title: zcodeCallTitle(facts), ...zcodeProseResult(facts) }),
  // The generic card, for a tool no vocabulary lists.
  other: (facts): ToolCallSpecVariant<'other'> => ({ kind: 'other', request: zcodeRequestFor('other', facts), ...zcodeGenericToolResult(facts) }),
  // UNREACHABLE, and the one entry here that a ZCode row could otherwise reach:
  // `zcodeReclassify` folds `''` to `other`, because a row with no tool name has no
  // label to separate it from an uncategorized one. The entry exists because the table
  // is total, and it states the same card at its OWN kind -- so a build that stops
  // folding draws the card rather than an empty row.
  unspecified: (facts): ToolCallSpecVariant<'unspecified'> => ({ kind: 'unspecified', request: zcodeRequestFor('unspecified', facts), ...zcodeGenericToolResult(facts) }),
  // The eleven kinds no ZCode tool takes: `ZCODE_TOOL_KINDS` maps no name to any of
  // them, and no display hint reaches one. With `''` above, twelve of the thirty kinds
  // are unreachable and the other eighteen are what ZCode produces.
  agents: zcodeArgumentsOnly('agents'),
  chart: zcodeArgumentsOnly('chart'),
  delete: zcodeArgumentsOnly('delete'),
  image: zcodeArgumentsOnly('image'),
  list: zcodeArgumentsOnly('list'),
  memory: zcodeArgumentsOnly('memory'),
  move: zcodeArgumentsOnly('move'),
  report: zcodeArgumentsOnly('report'),
  search: zcodeArgumentsOnly('search'),
  think: zcodeArgumentsOnly('think'),
  wait: zcodeArgumentsOnly('wait'),
}

/** The specification of one kind, read from the facts. The table covers `ToolKind`. */
function zcodeSpecFor<K extends ToolKind>(facts: ZCodeToolFacts, kind: K): { [P in K]: ToolCallSpecVariant<P> }[K] {
  return readToolCallSpec(ZCODE_TOOL_READERS, kind, facts)
}

/**
 * The reader of a kind ZCode never produces: the shared request, and the words the
 * call printed.
 *
 * A finished call with no typed answer takes `unparsedResult`, which states "this
 * build could not read the result into the kind's shape" -- true for a kind no ZCode
 * tool reaches.
 */
function zcodeArgumentsOnly<P extends ToolKind>(kind: P): (facts: ZCodeToolFacts) => ToolCallSpecVariant<P> {
  // The inner arrow states its OWN return type, although the signature above already
  // declares it. A contextual signature is not an annotated position, so without this
  // the literal escapes the excess-property check -- the same hole every table entry
  // closes, one level down.
  return (facts): ToolCallSpecVariant<P> => ({ kind, request: zcodeRequestFor(kind, facts), title: zcodeCallTitle(facts), ...zcodeUnreadResult(facts) })
}

/** The row's own header words: the description the arguments state, then the tool's name. */
function zcodeCallTitle(facts: ZCodeToolFacts): string {
  return pickString(facts.input, 'description') || facts.toolName || 'Tool'
}

/** The lifecycle of a kind whose answer this build cannot read into a shape. */
function zcodeUnreadResult(facts: ZCodeToolFacts): { result?: ToolFailureResult | UnparsedToolResult } {
  if (!facts.resultAvailable)
    return {}
  if (facts.failed)
    return { result: failedResult(facts.text) }
  return facts.text ? { result: unparsedResult(facts.text) } : {}
}

/** The result side every prose kind shares: none, the failure, or the words. */
function zcodeProseResult(facts: ZCodeToolFacts): { result?: ProseResult | ToolFailureResult } {
  if (!facts.resultAvailable)
    return {}
  return facts.failed ? { result: failedResult(facts.text) } : { result: proseResult(facts.text) }
}

/** The result side `glob` and `grep` share. */
function zcodeSearchResult(facts: ZCodeToolFacts): { result?: SearchResult | ToolFailureResult | UnparsedToolResult } {
  if (!facts.resultAvailable)
    return {}
  if (facts.failed)
    return { result: failedResult(facts.text) }
  const source = extractZCodeSearch(facts.row)
  if (!source)
    return { result: unparsedResult(facts.text) }
  // The search body draws its own notice, so the row keeps no flag.
  return { result: facts.truncated ? { ...source, truncated: true } : source }
}

/**
 * The result side `edit` and `write` share: the patch that was applied, the diff the
 * result display carries, or the words the tool printed.
 *
 * The SAME order as {@link zcodeFileChanges}, and that is what keeps the two halves of
 * one card describing one change. A finished row draws the result alone --
 * `RequestedChangesBody` stops drawing the request the moment a result lands -- so an
 * order that differed here would list three files while the call ran and one after it
 * ended. An `ApplyPatch` that succeeded applied exactly the envelope it sent, and that
 * envelope is the only source that states every file of it.
 *
 * The unparsed answer is the one an `ApplyPatch` reaches when its envelope does not
 * parse, and it is the only answer the two sources below can give it:
 * `extractZCodeFileDiff` reads the result display and the Edit and Write arguments, and
 * no patch. A row on that branch states the file name and the word "Applied" over no
 * diff at all.
 */
function zcodeFileChangeResult(facts: ZCodeToolFacts): { result?: FileChangeResult | ToolFailureResult | UnparsedToolResult } {
  if (!facts.resultAvailable)
    return {}
  if (facts.failed)
    return { result: failedResult(facts.text) }
  if (facts.patchChanges)
    return { result: { changes: facts.patchChanges } }
  if (facts.fileDiff)
    return { result: { changes: [facts.fileDiff] } }
  return { result: unparsedResult(facts.text) }
}

/** The result side the generic card states: the words and the sandbox pictures together. */
function zcodeGenericToolResult(facts: ZCodeToolFacts): { result?: GenericToolResult | ToolFailureResult } {
  if (!facts.resultAvailable)
    return {}
  if (facts.failed)
    return { result: failedResult(facts.text) }
  // The pictures ride INSIDE the content, which is what `ToolCallBase.images` states
  // for the generic trio: `GenericToolBody` never reads the call's own image list, so a
  // node-image row attached its pictures where nothing draws them.
  return {
    result: {
      content: [
        ...(facts.text ? [{ type: 'text' as const, text: facts.text }] : []),
        ...facts.nodeImages.map(source => ({ type: 'image' as const, source })),
      ],
    },
  }
}

/**
 * The changes an `edit` or a `write` states: the patch it sent, the landed diff, or the
 * change the arguments ask for.
 *
 * {@link ZCodeToolFacts.patchChanges} answers FIRST, and `ApplyPatch` is the only one
 * of the three tools whose whole change rides in one text argument. Several files can
 * travel in that one patch, and the result display carries at most one of them, so the
 * patch states more than either source below it. The envelope is the shared apply-patch
 * dialect, which `model/applyPatch.ts` reads for every runtime that sends it.
 *
 * KEPT for a failed call. `RequestedChangesBody` is the one place that decides whether
 * a failed row draws its diff, and it refuses -- so emptying the request here removed
 * nothing from the body and took the FILE NAME out of the row's title instead.
 */
function zcodeFileChanges(kind: 'edit' | 'write', args: Record<string, unknown>, facts: ZCodeToolFacts): FileEditDiff[] {
  if (facts.patchChanges)
    return facts.patchChanges
  const requested = facts.fileDiff ?? zcodeRequestedFileChange(kind, args)
  return requested ? [requested] : []
}

/** ZCode's own status words, in the shared outcome vocabulary. */
function zcodeTaskStatus(status: 'success' | 'failed' | 'waiting' | 'stopped'): TaskStatus {
  // `waiting` is ZCode's word for a task that has not answered; the shared vocabulary
  // spells that `running`, which is what the subagent card already called it.
  return status === 'success' ? 'completed' : status === 'waiting' ? 'running' : status
}

/** The action one cron call performs: the arguments' own word, then the tool's name. */
function zcodeTriggerAction(args: Record<string, unknown>, toolName: string): TriggerRequest['action'] {
  const stated = pickString(args, 'action')
  if (stated === 'create' || stated === 'delete' || stated === 'list' || stated === 'get' || stated === 'update' || stated === 'run')
    return stated
  return ZCODE_TRIGGER_ACTIONS.get(toolName) ?? 'other'
}

/** The change a scheduled file tool asks for, before any result lands. */
function zcodeRequestedFileChange(kind: 'edit' | 'write', input: Record<string, unknown>): FileEditDiff | null {
  const filePath = pickFirstString(input, TOOL_FILE_PATH_KEYS)
  if (!filePath)
    return null
  if (kind === 'write')
    return { filePath, operation: 'add', oldStr: '', newStr: pickString(input, 'content'), structuredPatch: null }
  const oldStr = pickString(input, 'old_string', undefined)
  const newStr = pickString(input, 'new_string', undefined)
  if (oldStr === undefined && newStr === undefined)
    return { filePath, oldStr: '', newStr: '', structuredPatch: null }
  return { filePath, oldStr: oldStr ?? '', newStr: newStr ?? '', structuredPatch: null }
}
