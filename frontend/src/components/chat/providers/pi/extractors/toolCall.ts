import type { ToolSpanRowRole } from '../../../model/row'
import type { ToolCall, ToolCallEnvelope, ToolCallLifecycleFacts, ToolCallSpecReaderTable, ToolCallSpecVariant, ToolFailureResult, ToolResult, UnparsedToolResult } from '../../../model/toolCall'
import type { ToolKind } from '../../../model/toolKind'
import type { ToolRequestByKind } from '../../../model/tools'
import type { AgentRequest, AgentRun } from '../../../model/tools/agent'
import type { FileChangeRequest } from '../../../model/tools/fileChange'
import type { SearchRequest } from '../../../model/tools/search'
import type { ToolRequestOverrides } from '../../defaultToolRequests'
import type { PiTodoSource } from './todo'
import type { PiToolExecution } from './toolCommon'
import type { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ImageResultSource } from '~/lib/imageBlocks'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { PI_EVENT, PI_TOOL } from '~/generated/contracts/pi-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { createToolCall } from '../../../model/createToolCall'
import { failedResult, proseResult, readToolCallSpec, unparsedResult } from '../../../model/toolCall'
import { deriveToolCallStatus } from '../../../model/toolCallLifecycle'
import { toolRequestFor } from '../../defaultToolRequests'
import { retainedOutcome, retainedRowIsFinal } from '../../registry'
import { PI_POWERSHELL_TOOL } from '../protocol'
import { piQuestionsFromArgs, piQuestionTitle } from '../questionSource'
import { piToolKind } from '../toolKinds'
import { piAgentRequest, piAgentResult } from './agent'
import { extractPiCommand, piCommandResult } from './execute'
import { extractPiRead, piFallbackDiffSources, piResolveDiffSources, resolvePiResultDiff } from './fileEdit'
import { piGenericToolSource, piMcpIdentity } from './generic'
import { piToolResultImages } from './image'
import { extractPiSearch } from './search'
import { piTodoSource } from './todo'
import { piExtractTool, piPairedRequest } from './toolCommon'
import { piWorkflowRequest, piWorkflowResult } from './workflow'

/** The display name of a tool whose wire name is not the word a reader wants. */
const PI_TOOL_LABELS: ReadonlyMap<string, string> = new Map<string, string>([
  [PI_TOOL.Bash, 'Bash'],
  [PI_POWERSHELL_TOOL, 'PowerShell'],
  [PI_TOOL.Read, 'Read'],
  [PI_TOOL.Write, 'Write'],
  [PI_TOOL.Edit, 'Edit'],
])

/**
 * One Pi tool call, resolved from the row and the two events that describe it.
 *
 * Pi's `tool_execution_end` carries no arguments, so a result row reads them from the
 * paired `tool_execution_start`. The to-do source is resolved ONCE here, because both
 * the body and the row's outcome read it: a to-do operation reports its failure in
 * `details.error` while Pi still reports `isError: false`.
 */
export interface PiToolRow {
  payload: Record<string, unknown>
  tool: PiToolExecution
  /** The paired `tool_execution_start`, or undefined when the store resolved none. */
  request: ParsedMessageContent | undefined
  /** The paired `tool_execution_end`, or undefined when the store resolved none. */
  result: ParsedMessageContent | undefined
  /** The resolved checklist of a to-do call, or null for every other tool. */
  todo: PiTodoSource | null
  /** True when this row is the last one of its call. */
  finished: boolean
  /** The call failed, whether Pi flagged it or stated it in its own fields. */
  isError: boolean
}

/** Build one tool row, or null for a row that is not a Pi tool call. */
export function piToolRow(
  parsed: unknown,
  request: ParsedMessageContent | undefined,
  result: ParsedMessageContent | undefined,
  completion?: MessageCompletion,
): PiToolRow | null {
  if (!isObject(parsed))
    return null
  const tool = piExtractTool(parsed)
  if (!tool)
    return null
  // A retained `tool_execution_start` is the call's END: the turn stopped before Pi
  // sent the completion event, so this frame is the last one there is.
  const finished = pickString(parsed, 'type') === PI_EVENT.ToolExecutionEnd || retainedRowIsFinal(completion)
  const todo = tool.toolName === PI_TOOL.Todo ? piTodoSource(parsed, request, result) : null
  return { payload: parsed, tool, request, result, todo, finished, isError: tool.isError || !!todo?.error }
}

/**
 * Which SIDE of its span one row draws: the request while the call runs, the answer
 * once it finished.
 *
 * A retained `tool_execution_start` counts as finished, so the last frame of an
 * interrupted turn draws the answer side. It would otherwise wait for a closing frame
 * that the runtime never sends.
 */
export function piToolSpanRowRole(row: PiToolRow): ToolSpanRowRole {
  return row.finished ? 'result' : 'request'
}

/**
 * Everything one Pi payload decision reads, collected ONCE for the row.
 *
 * The builder used to pick a kind and then reach back into the row for each fact the
 * branch wanted. That shape is why a branch could state a kind and fill no request at
 * all, and why two branches could read the same fact two different ways. The table
 * below takes these facts and nothing else.
 *
 * **`isError` and `status` answer two DIFFERENT questions. Never fold them together.**
 * `isError` is Pi's own flag on THIS frame. `status` is the row's one outcome word,
 * and it also carries LeapMux's own completion: a turn that ended while the call ran
 * leaves no final frame, so the outcome lives in the completion column and nowhere
 * else. A retained `tool_execution_start` whose message completion is `error`
 * therefore holds `status === 'failed'` beside `isError === false`, and both statements
 * are true -- the turn failed, and Pi flagged nothing.
 *
 * `status` has exactly ONE reader below: the `mcp` entry, which suppresses a
 * `statusOverride` the envelope already states. Every other decision reads `isError`.
 * A decision that asked `status === 'failed'` instead would take the error path on that
 * retained row -- and the `edit` and `write` entries state the result text as the
 * reason there. A retained START frame carries no result text, so the row would head a
 * `Failed` card over an empty reason, for a call Pi never faulted.
 */
export interface PiToolFacts {
  /** The frame this row was read from. The per-tool extractors take it whole. */
  payload: Record<string, unknown>
  /** The paired `tool_execution_start`, or undefined when the store resolved none. */
  request: ParsedMessageContent | undefined
  /** The paired `tool_execution_end`, or undefined when the store resolved none. */
  result: ParsedMessageContent | undefined
  /** Pi's own tool name, which is all Pi states about which tool ran. */
  toolName: string
  /** The tool's display name: the table's word, else the wire name. */
  label: string | undefined
  /** The arguments of the call, which only the opening event carries. */
  args: Record<string, unknown>
  /** The text the call returned, or the partial text a stopped turn left behind. */
  text: string
  /** The `details` record of THIS frame's own result. The `switch_mode` entry reads the plan from it. */
  details: Record<string, unknown> | undefined
  /** The resolved checklist of a to-do call, or null for every other tool. */
  todo: PiTodoSource | null
  /** The pictures the result carries. The `read` and `mcp` entries state them. */
  images: ImageResultSource[]
  /** True when this row is the last one of its call. */
  finished: boolean
  /** Pi flagged THIS frame as an error. Read the note above: this is not a status. */
  isError: boolean
  /** The call's lifecycle as RAW FACTS; the shared derivation owns the precedence. */
  lifecycle: ToolCallLifecycleFacts
  /** Whether Pi supplied a result body on this frame or its resolved result side. */
  resultAvailable: boolean
}

/** Collect everything the payload decisions read, in one pass. */
export function piToolFacts(row: PiToolRow, completion: MessageCompletion | undefined): PiToolFacts {
  const toolName = row.tool.toolName
  const pairedResult = piExtractTool(row.result?.parentObject)
  // The arguments live on the opening event alone, so a result row reads the paired
  // one. The frame's OWN arguments win where it carries any.
  const paired = pickObject(piPairedRequest(row.payload, row.request)?.parentObject, 'args')
  return {
    payload: row.payload,
    request: row.request,
    result: row.result,
    toolName,
    label: PI_TOOL_LABELS.get(toolName) ?? (toolName || undefined),
    args: Object.keys(row.tool.args).length > 0 ? row.tool.args : paired ?? {},
    text: row.tool.result?.text ?? row.tool.partialResult?.text ?? '',
    details: row.tool.result?.details,
    todo: row.todo,
    images: piToolResultImages(row.payload, undefined, row.request),
    finished: row.finished,
    isError: row.tool.isError,
    lifecycle: {
      frameStatus: 'unstated',
      providerOutcome: row.isError ? 'failed' : null,
      retainedOutcome: retainedOutcome(completion),
      rowFinal: row.finished,
      resultFrameLanded: pickString(row.payload, 'type') === PI_EVENT.ToolExecutionEnd || row.result !== undefined,
    },
    resultAvailable: row.tool.result !== undefined
      || row.tool.partialResult !== undefined
      || pairedResult?.result !== undefined
      || pairedResult?.partialResult !== undefined,
  }
}

/**
 * The kind one Pi row draws with, after the two corrections the tool NAME cannot make.
 *
 * A NAMED step, and the only place a kind changes. The payload builder used to re-enter
 * itself with the second kind, and only once the call finished -- so the row drew a
 * to-do card with an empty list while it ran, then became a Model Context Protocol card
 * the instant it ended. One call, two cards.
 *
 * EXPORTED so `createToolCall.test.ts` states each swap and each non-swap directly, as every
 * sibling provider's reclassification step already does.
 */
export function piReclassify(facts: PiToolFacts): ToolKind {
  const declared = piToolKind(facts.toolName)
  // The input: a tool name `PI_TOOL_KINDS` does not list. It is a Pi extension or a
  // Model Context Protocol bridge, and both answer with content blocks -- so they take
  // the generic card rather than the uncategorized kind, whose wrench states nothing
  // the agent ran.
  if (declared === 'unspecified')
    return 'mcp'
  // The input: a `todo` call whose checklist this build could not read -- an action the
  // reader does not know, or a snapshot that failed validation. It has no checklist to
  // draw, so it states its content blocks under the generic card instead.
  if (declared === 'todo' && !facts.todo)
    return 'mcp'
  return declared
}

/**
 * The kinds Pi reads from its OWN facts. Every other kind takes the shared
 * `DEFAULT_TOOL_REQUESTS` entry, which reads the arguments alone.
 *
 * This is the WHOLE deviation list, and `createToolCall.test.ts` pins it: an entry here is a
 * reading no other provider gets, so anything the arguments alone can supply in a
 * provider-NEUTRAL way belongs in the shared table where every provider reads it. Five
 * kinds Pi DOES produce are absent on purpose -- `read`, `grep`, `glob`, `list` and
 * `switch_mode` ask the shared table, because Pi spells their arguments the way every
 * other provider does.
 *
 * `question` reads the arguments alone and stays here all the same. Both halves of the
 * rule have to hold, and the second one does not: `piQuestionsFromArgs` parses Pi's own
 * question record, which no other provider sends.
 *
 * EVERY entry declares its own return type, for the reason {@link PI_TOOL_READERS}
 * gives at length: a contextual signature is not an annotated position, so an
 * un-annotated entry takes a stray key without a word.
 */
export const PI_TOOL_REQUEST_OVERRIDES: ToolRequestOverrides<PiToolFacts> = {
  // The launch card, which Pi states across the frame, the paired request and the paired
  // result; the shared entry sees the arguments alone.
  agent: (_args, facts): ToolRequestByKind['agent'] => piRowAgentRequest(facts),
  // The substitutions the opening event asked for, which Pi does not repeat in the
  // arguments of the row that reports them; the shared entry states no change at all.
  edit: (_args, facts): ToolRequestByKind['edit'] => piFileChangeRequest(facts),
  write: (_args, facts): ToolRequestByKind['write'] => piFileChangeRequest(facts),
  // Pi's own tool NAME picks the highlighter: `powershell` and `bash` send the same
  // arguments and only the name tells them apart. The shared entry sees no name.
  execute: (args, facts): ToolRequestByKind['execute'] => {
    // `pickString` answers `''` for a key the arguments do not hold, and the renderer
    // reads an EMPTY description as one the agent never sent.
    const description = pickString(args, 'description') || undefined
    return {
      command: pickString(args, 'command'),
      ...(facts.toolName === PI_POWERSHELL_TOOL ? { language: 'powershell' as const } : {}),
      ...(description !== undefined ? { description } : {}),
    }
  },
  // The server and the tool, which pi-mcp-adapter states in the paired RESULT and the
  // namespace proxy states in its own name; the shared entry reads two arguments Pi
  // never sends.
  mcp: (args, facts): ToolRequestByKind['mcp'] => {
    const identity = piMcpIdentity(facts.payload, facts.request, facts.result)
    return identity ? { server: identity.server, tool: identity.tool, args } : { server: '', tool: facts.toolName, args }
  },
  // Pi's own question RECORD, which all four of its question tools spell the same way
  // and no other provider spells at all -- so the shared entry states an empty list and
  // leaves the vocabulary to each provider. An empty request drew the bare wire name
  // over an empty body, and the reader lost both the question and every option.
  question: (args): ToolRequestByKind['question'] => ({ questions: piQuestionsFromArgs(args) }),
  // The checklist this row resolved, which Pi sends in the result rather than in the
  // arguments; the shared entry states an empty list.
  todo: (_args, facts): ToolRequestByKind['todo'] => {
    const todo = facts.todo
    // `note` rides only when a checklist was resolved, never as an explicit undefined.
    return { items: todo?.list.todos ?? [], ...(todo ? { note: todo.description } : {}) }
  },
}

/** One kind's declared request: Pi's own reading where it states one, the shared table's elsewhere. */
function piRequestFor<K extends ToolKind>(kind: K, facts: PiToolFacts): ToolRequestByKind[K] {
  return toolRequestFor(kind, facts.args, facts, PI_TOOL_REQUEST_OVERRIDES)
}

/**
 * The two header fields every kind carries: the tool's display name, and the words
 * above it.
 *
 * A kind that composes its own header words states them after this. `execute` takes
 * NO title: the command is the header on every other surface, and a title here would
 * put `Bash` above the very command it ran.
 */
function piHeader(facts: PiToolFacts): { label?: string, title: string } {
  // `label` rides only when the table or the wire name stated one, never as an explicit undefined.
  return { ...(facts.label !== undefined ? { label: facts.label } : {}), title: facts.label ?? 'Tool' }
}

/**
 * The declared payload of a kind Pi never produces: that kind's own request from the
 * shared table, and no result.
 *
 * Every kind outside `PI_TOOL_KINDS` reaches this, and none of them draws today. It is
 * what makes the table TOTAL, and totality is the point: the builder used to end with a
 * fallthrough that answered `kind: 'other'` and a dump of the arguments, so a kind added
 * to `PI_TOOL_KINDS` but not to the builder drew a wrench where its own card belongs.
 * Here it draws its own card from the first row.
 */
function piDeclaredOnly<K extends ToolKind>(kind: K): (facts: PiToolFacts) => ToolCallSpecVariant<K> {
  // The inner arrow states its OWN return type, although the signature above already
  // declares it. A contextual signature is not an annotated position, so without this
  // the literal escapes the excess-property check -- the same hole every table entry
  // closes, one level down.
  return (facts): ToolCallSpecVariant<K> => ({ kind, ...piHeader(facts), request: piRequestFor(kind, facts) })
}

/**
 * The words a finished call printed, branded for the outcome the ROW states.
 *
 * A failed row never carries the unparsed brand. That brand states "the call completed
 * and this build could not read the payload", which a failed call did not do, and the
 * model refuses the pair -- a call that stated it degraded to the uncategorized card and
 * lost its kind, its request and its words together.
 *
 * A retained row whose TURN failed is what reaches here with `status === 'failed'` and
 * Pi's own flag unset, and the words it left behind are the reason the call ended with.
 * With no words at all it states NO result: an empty reason says less than the row's own
 * Error header, and the status admits an absent result.
 */
function piUnreadResult(facts: PiToolFacts, text: string): UnparsedToolResult | ToolFailureResult | undefined {
  if (!text)
    return undefined
  if (deriveToolCallStatus(facts.lifecycle, facts.resultAvailable) !== 'failed')
    return unparsedResult(text)
  return failedResult(text)
}

/**
 * The file-change card's two halves. `edit` and `write` answer the same way and declare
 * the same request and result, so one reader serves both -- but each entry states its
 * OWN kind, because a generic kind parameter would put the pair beyond the checker
 * again, which is what this table exists to prevent.
 */
function piFileChangeParts(kind: 'edit' | 'write', facts: PiToolFacts): { request: FileChangeRequest, result?: ToolResult<'edit' | 'write'> } {
  const request = piRequestFor(kind, facts)
  if (!facts.resultAvailable)
    return { request }
  // Pi's OWN flag, never the row's status. A retained row whose turn failed carries
  // `status === 'failed'` and no flag, so it goes past this guard and keeps the
  // substitutions it asked for.
  //
  // The REQUEST stays here too. `RequestedChangesBody` refuses to draw a failed call's
  // diff for every provider, so the list adds nothing to the body -- but the row's
  // TITLE is composed from it, and an empty list heads a failed edit with the bare word
  // `Edit` and no way to tell which file the call was about.
  if (facts.isError)
    return { request, result: failedResult(facts.text) }
  const sources = piResolveDiffSources(facts.payload, facts.request)
  if (sources.length > 0)
    return { request, result: { changes: sources } }
  // A diff this build cannot read stays unparsed: the row draws the raw diff, and
  // the Copy button hands over the same words. No words state no result at all.
  const unread = piUnreadResult(facts, resolvePiResultDiff(facts.payload, facts.args).rawDiff || facts.text)
  return unread !== undefined ? { request, result: unread } : { request }
}

/** The search card's two halves. `grep` and `glob` declare the same pair, as above. */
function piSearchParts(kind: 'grep' | 'glob', facts: PiToolFacts): { request: SearchRequest, result?: ToolResult<'grep' | 'glob'> } {
  const request = piRequestFor(kind, facts)
  if (!facts.resultAvailable)
    return { request }
  if (facts.isError)
    return { request, result: failedResult(facts.text) }
  const search = extractPiSearch(facts.payload)
  if (search)
    return { request, result: search }
  const unread = piUnreadResult(facts, facts.text)
  return unread !== undefined ? { request, result: unread } : { request }
}

/**
 * One reader for each kind, each checked against its OWN kind's request and result.
 *
 * A chain of `if (kind === ...)` cannot do this. TypeScript narrows the VALUE it tests
 * and never the type parameter, so the builder declared one union return and filled the
 * request inside the branch that had just picked the kind. A branch could therefore
 * state a kind and fill no request at all -- a defect class this table removes. Here
 * each entry's value type mentions its own `K`, so a missing field and a field of the
 * wrong shape are both compile errors at the entry that states them.
 *
 * An EXCESS key is not, and the mapped type cannot make it one. The contextual signature
 * this table supplies is not an annotated position: TypeScript infers an un-annotated
 * arrow's return type from the literals it returns, so the object loses its freshness
 * before any property is checked. That is why every entry above declares its own return
 * type. `'read': facts => ({ kind: 'read', request, patchText })` compiles with the
 * undeclared key; `'read': (facts): ToolCallSpecVariant<'read'> =>` rejects it.
 *
 * The same rule holds one step in: do NOT lift a request into an un-annotated `const`
 * and return it by reference. A variable is not a fresh literal, so the check never runs
 * on it -- which is how a `patchText` key once reached the model that no renderer could
 * read. `toolTableEntriesAreAnnotated.test.ts` keeps both forms out.
 *
 * The LIFECYCLE stays per entry, unlike the Agent Client Protocol's ladder: Pi's kinds
 * do not agree on it. A checklist and a question state their content before the call
 * returns, `switch_mode` answers a failure and its prose at once, and `execute` draws a
 * failed command's own output beside its exit code.
 *
 * The table is EXPORTED for its own cases in `createToolCall.test.ts`, which pin the three
 * statements no type makes: the keys are exactly `TOOL_KINDS`, each entry answers at
 * the key that states it, and every kind outside {@link PI_TOOL_REQUEST_OVERRIDES}
 * fills the shared declared request. The last one is the only mechanical check that the
 * overrides map has not grown past its deviations.
 */
export const PI_TOOL_READERS: ToolCallSpecReaderTable<PiToolFacts> = {
  agent: (facts): ToolCallSpecVariant<'agent'> => {
    const request = piRequestFor('agent', facts)
    return {
      kind: 'agent',
      ...piHeader(facts),
      // What the subagent was asked to do heads the row, because the tool name says
      // only that a subagent ran.
      title: request.description,
      request,
      ...(facts.resultAvailable ? { result: { agents: [piAgentRun(facts)] } } : {}),
    }
  },
  todo: (facts): ToolCallSpecVariant<'todo'> => {
    const request = piRequestFor('todo', facts)
    const header = piHeader(facts)
    // The checklist's own header words: `Create task: ...`, or the count of a listing.
    const title = facts.todo?.list.title ?? header.title
    const metadata = facts.todo?.metadata.length ? facts.todo.metadata : undefined
    // Pi reports a refused to-do operation in `details.error` and still flags the call
    // a success, so the reason comes from the checklist rather than from the flag.
    if (facts.todo?.error)
      return { kind: 'todo', ...header, title, ...(metadata !== undefined ? { metadata } : {}), request, result: failedResult(facts.todo.error) }
    // The request's own items and note, so the two halves of the card cannot state two
    // different readings of one checklist.
    const emptyText = facts.todo?.list.emptyText
    return {
      kind: 'todo',
      ...header,
      title,
      ...(metadata !== undefined ? { metadata } : {}),
      request,
      ...(facts.resultAvailable
        ? {
            result: {
              items: request.items,
              ...(emptyText !== undefined ? { emptyText } : {}),
              ...(request.note !== undefined ? { note: request.note } : {}),
            },
          }
        : {}),
    }
  },
  execute: (facts): ToolCallSpecVariant<'execute'> => {
    const request = piRequestFor('execute', facts)
    const label = facts.label
    if (!facts.resultAvailable)
      return { kind: 'execute', ...(label !== undefined ? { label } : {}), request }
    const resolved = extractPiCommand(facts.payload)
    if (!resolved) {
      const unread = piUnreadResult(facts, facts.text)
      return { kind: 'execute', ...(label !== undefined ? { label } : {}), request, ...(unread !== undefined ? { result: unread } : {}) }
    }
    // Pi reports a stop and a timeout as errors, so the envelope's own status words
    // BOTH as `failed` -- the row then headed a command the reader stopped "Error",
    // with no exit code, and with the marker that explained it stripped from the body.
    // The marker parser is the only reader that knows, so it states the word here.
    return {
      kind: 'execute',
      ...(label !== undefined ? { label } : {}),
      request,
      result: { commands: [piCommandResult(resolved)], unresolvedTerminals: [] },
      ...(resolved.cancelled ? { statusOverride: 'cancelled' as const } : {}),
    }
  },
  read: (facts): ToolCallSpecVariant<'read'> => {
    const request = piRequestFor('read', facts)
    const header = piHeader(facts)
    if (!facts.resultAvailable)
      return { kind: 'read', ...header, request }
    if (facts.isError)
      return { kind: 'read', ...header, request, result: failedResult(facts.text) }
    const read = extractPiRead(facts.payload, facts.args)
    if (read)
      return { kind: 'read', ...header, request, result: read, images: facts.images }
    const unread = piUnreadResult(facts, facts.text)
    return { kind: 'read', ...header, request, ...(unread !== undefined ? { result: unread } : {}) }
  },
  edit: (facts): ToolCallSpecVariant<'edit'> => ({ kind: 'edit', ...piHeader(facts), ...piFileChangeParts('edit', facts) }),
  write: (facts): ToolCallSpecVariant<'write'> => ({ kind: 'write', ...piHeader(facts), ...piFileChangeParts('write', facts) }),
  grep: (facts): ToolCallSpecVariant<'grep'> => ({ kind: 'grep', ...piHeader(facts), ...piSearchParts('grep', facts) }),
  glob: (facts): ToolCallSpecVariant<'glob'> => ({ kind: 'glob', ...piHeader(facts), ...piSearchParts('glob', facts) }),
  list: (facts): ToolCallSpecVariant<'list'> => {
    const request = piRequestFor('list', facts)
    const header = piHeader(facts)
    if (!facts.resultAvailable)
      return { kind: 'list', ...header, request }
    if (facts.isError)
      return { kind: 'list', ...header, request, result: failedResult(facts.text) }
    // Pi lists a directory through the same tool that searches it, so the listing
    // arrives as the search body's file names.
    const search = extractPiSearch(facts.payload)
    if (search) {
      const notice = search.notice
      return { kind: 'list', ...header, request, result: { entries: search.filenames.map(path => ({ path })), truncated: search.truncated, ...(notice !== undefined ? { notice } : {}) } }
    }
    const unread = piUnreadResult(facts, facts.text)
    return { kind: 'list', ...header, request, ...(unread !== undefined ? { result: unread } : {}) }
  },
  question: (facts): ToolCallSpecVariant<'question'> => {
    const request = piRequestFor('question', facts)
    const header = piHeader(facts)
    const title = piQuestionTitle(request.questions) ?? header.title
    // Pi returns the chosen option as text rather than as a structured answer, so the
    // frame's own words ARE the answer, under the question's own header.
    return {
      kind: 'question',
      ...header,
      title,
      request,
      ...(facts.resultAvailable && facts.text ? { result: { answers: [{ header: request.questions[0]?.header || title || 'Answer', answer: facts.text }] } } : {}),
    }
  },
  switch_mode: (facts): ToolCallSpecVariant<'switch_mode'> => {
    // The plan the tool wrote, which it states in its own `details` rather than in a
    // content block.
    const plan = pickString(facts.details, 'plan').trim()
    return {
      kind: 'switch_mode',
      ...piHeader(facts),
      request: piRequestFor('switch_mode', facts),
      ...(facts.resultAvailable ? { result: proseResult(plan || facts.text, 'markdown') } : {}),
      ...(facts.isError ? { statusOverride: 'failed' as const, result: failedResult(facts.text) } : {}),
    }
  },
  mcp: (facts): ToolCallSpecVariant<'mcp'> => {
    const request = piRequestFor('mcp', facts)
    const header = piHeader(facts)
    // NO result until the call finishes, exactly as every other entry here waits.
    // `ToolMessage` draws the LIVE output the worker broadcasts only while the row is
    // `in_progress` AND its result is absent, so a result attached to a running call
    // replaces the streaming tail with an empty card. This entry is the one that pays
    // for it: `piReclassify` routes every tool `PI_TOOL_KINDS` does not hold to `mcp`,
    // so the rule covers each Pi extension and each Model Context Protocol bridge.
    // Invariant I1 in `~/test-support/toolVocabulary` states the same rule from the
    // other side, and `toolResults.test.ts` walks every opening frame through it.
    if (!facts.resultAvailable)
      return { kind: 'mcp', ...header, request }
    // `piGenericToolSource` answers null for a payload that states no tool call, and no
    // ROW carries one: `piToolRow` refused that payload before this table ran. The
    // optional reads below are the checker's price for a state the row cannot reach.
    const source = piGenericToolSource(facts.payload, facts.request, facts.result)
    // The pictures ride in the content blocks alone: the row numbers them from
    // there, and a second list of its own would count every one twice.
    const content = source?.content.length
      ? source.content
      : facts.images.map(image => ({ type: 'image' as const, source: image }))
    const structuredJson = source?.structuredJson
    const error = source?.error
    const durationMs = source?.durationMs
    return {
      kind: 'mcp',
      ...header,
      request,
      result: {
        content,
        ...(structuredJson !== undefined ? { structuredJson } : {}),
        ...(error !== undefined ? { error } : {}),
        ...(durationMs !== undefined ? { durationMs } : {}),
      },
      ...(source?.failed && deriveToolCallStatus(facts.lifecycle, facts.resultAvailable) !== 'failed' ? { statusOverride: 'failed' as const } : {}),
    }
  },
  // The eighteen kinds Pi states no tool for. Each still declares its own request, so
  // the day one of them arrives it draws its own card rather than a dump.
  unspecified: piDeclaredOnly('unspecified'),
  agents: piDeclaredOnly('agents'),
  chart: piDeclaredOnly('chart'),
  delete: piDeclaredOnly('delete'),
  fetch: piDeclaredOnly('fetch'),
  image: piDeclaredOnly('image'),
  memory: piDeclaredOnly('memory'),
  message: piDeclaredOnly('message'),
  move: piDeclaredOnly('move'),
  other: piDeclaredOnly('other'),
  report: piDeclaredOnly('report'),
  search: piDeclaredOnly('search'),
  skill: piDeclaredOnly('skill'),
  task: piDeclaredOnly('task'),
  think: piDeclaredOnly('think'),
  trigger: piDeclaredOnly('trigger'),
  wait: piDeclaredOnly('wait'),
  web_search: piDeclaredOnly('web_search'),
}

/** One Pi tool call, as the kind-discriminated pair. */
export function piToolCall(row: PiToolRow, completion?: MessageCompletion): ToolCall {
  const facts = piToolFacts(row, completion)
  const envelope: ToolCallEnvelope = { id: row.tool.toolCallId, name: facts.toolName, lifecycle: facts.lifecycle }
  return createToolCall(envelope, readToolCallSpec(PI_TOOL_READERS, piReclassify(facts), facts))
}

/** The subagent card of a launch, a workflow run, or one of the two control tools. */
function piRowAgentRequest(facts: PiToolFacts): AgentRequest {
  return facts.toolName === PI_TOOL.SubagentWorkflow
    ? piWorkflowRequest(facts.payload, facts.request, facts.result)
    : piAgentRequest(facts.payload, facts.request)
}

/** The subagent report one finished agent call states. */
function piAgentRun(facts: PiToolFacts): AgentRun {
  return facts.toolName === PI_TOOL.SubagentWorkflow
    ? piWorkflowResult(facts.payload, facts.request)
    : piAgentResult(facts.payload, facts.request)
}

/**
 * The changes an edit or write call asked for, read from its opening event.
 *
 * A RESOLVED request is authoritative even when it states nothing, so the row's own
 * frame serves the call that resolved none rather than standing beside one.
 */
function piFileChangeRequest(facts: PiToolFacts): FileChangeRequest {
  return { changes: piFallbackDiffSources(facts.toolName, facts.request ? facts.request.parentObject : facts.payload) }
}
