import type { FileChangeEntry, FileEditDiff } from '../../../model/fileEditDiff'
import type { ChatRow, ToolSpanRowRole } from '../../../model/row'
import type { ToolCall, ToolCallLifecycleFacts, ToolCallSpecReader, ToolCallSpecReaderTable, ToolCallSpecVariant, ToolFailureResult } from '../../../model/toolCall'
import type { ToolCallStatus } from '../../../model/toolCallStatus'
import type { ToolKind } from '../../../model/toolKind'
import type { ExecuteRequest } from '../../../model/tools/execute'
import type { FileChangeRequest, FileChangeResult } from '../../../model/tools/fileChange'
import type { ImageRequest } from '../../../model/tools/image'
import type { WebSearchResult } from '../../../model/tools/webSearch'
import type { ToolRequestOverrides } from '../../defaultToolRequests'
import type { RowExtractionInput, ToolSpanContext } from '~/components/chat/rowExtractionTypes'
import type { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ImageResultSource } from '~/lib/imageBlocks'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { CODEX_ITEM, CODEX_ITEM_FIELD, CODEX_METHOD } from '~/generated/contracts/codex-protocol'
import { prettifyJson } from '~/lib/jsonFormat'
import { isObject, pickNumber, pickObject, pickString, stringArray } from '~/lib/jsonPick'
import { markdownBulletList } from '~/lib/markdownList'
import { pluralize } from '~/lib/plural'
import { leapmuxPlanExecutionRow, leapmuxUserRow } from '../../../leapmuxRows'
import { createToolCall } from '../../../model/createToolCall'
import { fileEditDiffFromOldNew, fileEditDiffsFromChanges } from '../../../model/fileEditDiff'
import { mcpToolCallDisplayName } from '../../../model/mcpToolCall'
import { toolCallRow } from '../../../model/row'
import { failedResult, proseResult, readToolCallSpec } from '../../../model/toolCall'
import { SYNTHETIC_TOOL_LIFECYCLE } from '../../../model/toolCallLifecycle'
import { toolCallStatus } from '../../../model/toolCallStatus'
import { formatDuration, humanizeWireWord } from '../../../rendererUtils'
import { toolRequestFor } from '../../defaultToolRequests'
import { retainedOutcome } from '../../registry'
import { CODEX_INTERNAL_TOOL, CODEX_STATUS } from '../itemVocabulary'
import { isCodexFinishedStatus } from '../status'
import { codexAgentCounterpart, codexAgentRequest, codexAgentResults, resolveCodexAgentItem } from './agent'
import { codexCommandActionsFromItem, codexCommandFromItem, codexRawExecCall, codexUnwrapCommand } from './execute'
import { codexChangeKind } from './fileChange'
import { codexGeneratedImage, codexItemPath, codexViewedImage } from './image'
import { extractItem } from './item'
import { codexMcpFromItem } from './mcp'
import { codexPlanItemMarkdown, codexTurnPlanParams, codexTurnPlanTodos } from './plan'
import { codexWebSearchActionFromItem } from './webSearch'

/**
 * Read one Codex row into the shared row model.
 *
 * Typed app-server items use the per-kind reader table.
 * Native code execution uses a raw request and its matching raw output.
 */
export function codexExtractRow(input: RowExtractionInput): ChatRow | null {
  const { category, resolved: parsed, span } = input
  switch (category.kind) {
    case 'assistant_text': {
      // The same reader the classifier used, so the two layers state one answer:
      // Codex persists an item under three envelopes and `extractItem` knows all three.
      const text = pickString(extractItem(parsed.parentObject), 'text')
      return text ? { kind: 'assistant-text', text } : { kind: 'hidden' }
    }
    case 'assistant_thinking':
      return codexReasoningRow(extractItem(parsed.parentObject) ?? undefined)
    case 'assistant_plan': {
      // The same reader the classifier used, so the two layers state one answer.
      const plan = codexPlanItemMarkdown(extractItem(parsed.parentObject))
      return plan === null ? { kind: 'hidden' } : { kind: 'assistant-plan', text: plan }
    }
    case 'user_content':
      return leapmuxUserRow(parsed.parentObject)
    case 'plan_execution':
      return leapmuxPlanExecutionRow(parsed.parentObject)
    case 'tool_use':
      return codexToolSpanRow(parsed, span, input.completion)
    default:
      return null
  }
}

/**
 * The thinking row a Codex `reasoning` item becomes.
 *
 * Codex reports its reasoning twice over: a `summary` of short lines, and the raw
 * `content` when it sends no summary. The summary becomes a MARKDOWN list, so the
 * shared thinking bubble draws it as a list without a second renderer that owns its
 * own `<ul>` -- and so a reader sees the same bubble on Codex and on Claude.
 */
function codexReasoningRow(item: Record<string, unknown> | undefined): ChatRow {
  const text = codexReasoningText(item)
  return text ? { kind: 'assistant-thinking', text } : { kind: 'hidden' }
}

/**
 * The words one `reasoning` item states, or '' when it states none.
 *
 * The CLASSIFIER reads this too, so the two cannot disagree about whether the item
 * draws. Counting the raw `summary` and `content` arrays instead measured a row for an
 * item whose entries were blank strings or objects, and hid one that states its words
 * in the third spelling -- `text` -- which the count never looked at.
 */
export function codexReasoningText(item: Record<string, unknown> | undefined): string {
  const summary = stringArray(item?.summary).filter(entry => entry.trim().length > 0)
  const content = stringArray(item?.content).filter(entry => entry.trim().length > 0).join('\n')
  const text = pickString(item, 'text')
  return summary.length > 0 ? markdownBulletList(summary) : content || (text.trim() ? text : '')
}

/** Whether one reasoning item contains visible text, without building Markdown. */
export function codexReasoningHasText(item: Record<string, unknown> | undefined): boolean {
  return stringArray(item?.summary).some(entry => entry.trim().length > 0)
    || stringArray(item?.content).some(entry => entry.trim().length > 0)
    || pickString(item, 'text').trim().length > 0
}

/** Whether an item is a complete one-row result without a status word. */
function isCodexAtomicResultItem(type: string): boolean {
  return type === CODEX_ITEM.Sleep
    || type === CODEX_ITEM.EnteredReviewMode
    || type === CODEX_ITEM.ExitedReviewMode
    || type === CODEX_ITEM.HookPrompt
    || type === CODEX_ITEM.FunctionCallOutput
}

/**
 * The to-do row a `turn/plan/updated` notification becomes.
 *
 * The classification hands the WHOLE notification, because the plan is not an item and
 * has no id of its own; the plan itself sits under `params`.
 *
 * This is the ONE tool row Codex builds outside {@link CODEX_TOOL_READERS}, and the
 * reason is that a notification is not an item: it carries no `type`, no `status` and
 * no span of its own, so {@link CodexToolFacts} cannot describe it. The table's `todo`
 * entry therefore states the shared declared request, which no Codex row reads.
 */
function codexTurnPlanRow(notification: Record<string, unknown>): ChatRow | null {
  const params = codexTurnPlanParams(notification)
  const todos = codexTurnPlanTodos(params)
  if (todos === null)
    return null
  const explanation = pickString(params, 'explanation').trim()
  // The explanation says WHY the plan moved, which the list itself does not.
  const title = todos.length > 0
    ? (explanation ? `${pluralize(todos.length, 'task')} - ${explanation}` : pluralize(todos.length, 'task'))
    : ''
  const call = createToolCall(
    { id: '', name: CODEX_INTERNAL_TOOL.TURN_PLAN, lifecycle: SYNTHETIC_TOOL_LIFECYCLE },
    {
      kind: 'todo',
      label: 'Plan Update',
      title,
      request: { items: todos },
      result: { items: todos },
    },
  )
  // A plan update is ONE row: the frame states the whole list, so the span holds no
  // request and no result beside it.
  return toolCallRow(call, 'result', { request: false, result: false })
}

/**
 * The kind each Codex item takes.
 *
 * EVERY type `CODEX_ITEM` lists reaches a kind. `other` is left for an item type a
 * release after this build adds, and `itemVocabulary.test.ts` fails the suite when
 * a LISTED type takes it: the uncategorized row draws a wrench above a dump of the
 * item, and identifies nothing Codex did.
 *
 * `webSearch` reads `fetch` for an opened page and `web_search` for everything else,
 * because its request is a query and its result is links.
 *
 * The answer is FINAL. Codex reclassifies no item: the kind decided here is the key
 * {@link CODEX_TOOL_READERS} is read under, and each reader answers at its own kind,
 * so the kind a row draws is always the kind this function gave it.
 */
export function codexItemKind(item: Record<string, unknown>, fileChanges?: FileChangeEntry[]): ToolKind {
  switch (pickString(item, 'type')) {
    case CODEX_ITEM.CommandExecution: return 'execute'
    case CODEX_ITEM.FileChange: return codexFileChangeKind(fileChanges ?? codexChangeEntries(item))
    case CODEX_ITEM.CollabAgentToolCall: return 'agent'
    case CODEX_ITEM.ImageView: return 'read'
    case CODEX_ITEM.ImageGeneration: return 'image'
    case CODEX_ITEM.WebSearch:
      return codexWebSearchActionFromItem(item)?.type === 'openPage' ? 'fetch' : 'web_search'
    // A call that reaches a server or a dynamic namespace, and the output of one.
    // All three draw the shared Model Context Protocol card.
    case CODEX_ITEM.McpToolCall:
    case CODEX_ITEM.DynamicToolCall:
    case CODEX_ITEM.FunctionCallOutput: return 'mcp'
    // Both review markers change the mode the turn runs in, which is the switch the
    // Agent Client Protocol spells and every other provider's plan toggle answers.
    case CODEX_ITEM.EnteredReviewMode:
    case CODEX_ITEM.ExitedReviewMode: return 'switch_mode'
    case CODEX_ITEM.Sleep: return 'wait'
    // A hook is the session's own extensibility surface, which is what a skill is.
    case CODEX_ITEM.HookPrompt: return 'skill'
    default: return 'other'
  }
}

/**
 * The one-file change kinds: a write adds, a delete removes, the rest edit.
 *
 * It reads {@link codexChangeEntries}, which is the same list the specification builds its
 * request from. A second reading of the raw `changes` array answered for entries the
 * request then dropped, so the row could take the `write` kind and state no file.
 */
function codexFileChangeKind(entries: FileChangeEntry[]): ToolKind {
  const [only] = entries
  if (entries.length !== 1 || only === undefined)
    return 'edit'
  switch (only.operation) {
    case 'add': return 'write'
    case 'delete': return 'delete'
    case 'move': return 'move'
    default: return 'edit'
  }
}

/**
 * The file operations one item's `changes` list states, in the shared change shape.
 *
 * An entry that states NO file is dropped, and both halves below read this one list, so
 * neither can draw a change the reader cannot identify.
 */
function codexChangeEntries(item: Record<string, unknown>): FileChangeEntry[] {
  return (Array.isArray(item.changes) ? item.changes : [])
    .filter(isObject)
    .map((change) => {
      const kind = codexChangeKind(change)
      const movePath = pickString(pickObject(change, 'kind'), 'movePath')
      const patchOrBody = pickString(change, 'diff')
      return {
        // A move states its DESTINATION in `kind.movePath` and its source in `path`.
        filePath: movePath || pickString(change, 'path'),
        ...(movePath ? { previousPath: pickString(change, 'path') } : {}),
        operation: kind === 'add' ? 'add' as const : kind === 'delete' ? 'delete' as const : movePath ? 'move' as const : 'edit' as const,
        // Codex sends a UNIFIED patch for an update and the whole file body for an add
        // or a delete, both under `diff`.
        ...(kind !== 'add' && kind !== 'delete' ? { patch: patchOrBody } : {}),
        ...(kind === 'add' || kind === 'delete' ? { content: patchOrBody } : {}),
      }
    })
    .filter(entry => entry.filePath !== '')
}

/**
 * Read one requested change for every file entry.
 *
 * fileEditDiffsFromChanges omits an update whose body cannot be parsed as a diff.
 * A result can omit that update, but the request must retain its file.
 * A declined update still needs the file path in its header, even when no readable diff exists.
 * Otherwise, the reader cannot identify the file that the agent refused to change.
 */
function codexRequestedChanges(entries: FileChangeEntry[]): FileEditDiff[] {
  return entries.map((entry) => {
    // ONE entry at a time, so a body this build CAN read still reaches the pending
    // row's diff, and one it cannot takes only its own body away.
    const [drawn] = fileEditDiffsFromChanges([entry])
    return drawn ?? {
      ...fileEditDiffFromOldNew(entry.filePath, '', ''),
      operation: entry.operation,
      ...(entry.previousPath !== undefined ? { previousPath: entry.previousPath } : {}),
    }
  })
}

/** The words each status-shaped Codex item puts in its own header. */
const CODEX_STATUS_ITEM_TITLES: Record<string, string> = {
  [CODEX_ITEM.Sleep]: 'Sleep',
  [CODEX_ITEM.EnteredReviewMode]: 'Entered review mode',
  [CODEX_ITEM.ExitedReviewMode]: 'Exited review mode',
}

/** The header words the table above states for one item type, or none for a type it omits. */
function codexStatusItemTitle(type: string): string | undefined {
  // `Object.hasOwn`, not a bare read: `type` comes straight off the wire, and a value
  // that spells an `Object.prototype` member answers with a function -- which the
  // header would then draw as that function's own source text. The facts read this
  // ONCE, so no reader can reach the raw table and re-open that hole.
  return Object.hasOwn(CODEX_STATUS_ITEM_TITLES, type) ? CODEX_STATUS_ITEM_TITLES[type] : undefined
}

/**
 * The note a status-shaped item carries under its header.
 *
 * `review` rides both review markers and is an object on the wire, so it is
 * pretty-printed rather than read field by field: the runtime states the review's
 * own shape and a reader gets all of it either way. `durationMs` is a NUMBER, and
 * reading it with `pickString` answered the empty string for every sleep -- so the
 * one fact a sleep row carries never reached the reader.
 */
function codexStatusDetail(item: Record<string, unknown>): string {
  const review = item.review
  if (isObject(review))
    return prettifyJson(review)
  const reviewText = pickString(item, 'review')
  if (reviewText)
    return reviewText
  const durationMs = pickNumber(item, 'durationMs', undefined)
  return durationMs === undefined ? '' : formatDuration(durationMs)
}

/**
 * The pictures one item carries.
 *
 * Two item types hold one: `imageGeneration` holds the picture it made, and
 * `imageView` holds the file it looked at. Each reader answers null for the other's
 * type, so at most one of them states a picture and the pair collapses to one list.
 */
function codexItemImages(item: Record<string, unknown>): ImageResultSource[] {
  const image = codexGeneratedImage(item) ?? codexViewedImage(item)
  return image ? [image] : []
}

/**
 * Resolve the facts that typed item readers need before a specification reader runs.
 *
 * Each CODEX_TOOL_READERS entry reads these facts only:
 * - The complete item and its discriminators.
 * - The call lifecycle and span.
 * - The shared title and detail.
 * - The images and normalized file changes.
 * - The complete output stream.
 */
export interface CodexToolFacts {
  /**
   * The complete argument record of a typed app-server item.
   *
   * These items keep call fields at their top level, outside an arguments object.
   * Shared request readers therefore use the item's keys directly.
   * Native execution uses raw request input and dispatches before this table.
   */
  item: Record<string, unknown>
  /** A typed app-server item's type identifies its tool. Native execution dispatches before this table. */
  type: string
  /**
   * The item's original status word.
   *
   * parseCodexStatus normalizes words that two readers must retain.
   * A file change uses completed to determine whether its changes applied.
   * An agent uses interrupted to retain cancellation. The status parser would normalize that word to inProgress.
   */
  status: string
  /** The kind the item takes. This is the key the reader table is read under. */
  kind: ToolKind
  /**
   * True when any of these conditions ends the call:
   * - The row closes its span.
   * - The item reports a finished status.
   * - The Worker stores a completion value.
   * - The resolver supplies the result side.
   *
   * A retained row can still contain inProgress after the reader stops the call.
   * Checking only that word would discard its partial output.
   */
  finished: boolean
  /** The row's span context. An agent call reads its counterpart from here. */
  sides: ToolSpanContext
  /** The header words a status-shaped item states, or none for a type the table omits. */
  statusTitle: string | undefined
  /** The note a status-shaped item carries under its header. */
  detail: string
  /** The item's generated or viewed images. An item without images keeps an empty list. */
  images: ImageResultSource[]
  /** Normalize file changes once. The kind and request use this list. The result uses it also. */
  fileChanges: FileChangeEntry[]
  /** Everything Codex aggregated from the call's own output stream, untrimmed. */
  aggregatedOutput: string
}

/** Read one item into the facts every specification reader shares. */
export function codexToolFacts(item: Record<string, unknown>, finished: boolean, sides: ToolSpanContext): CodexToolFacts {
  const type = pickString(item, 'type')
  const fileChanges = type === CODEX_ITEM.FileChange ? codexChangeEntries(item) : []
  return {
    item,
    type,
    status: pickString(item, 'status'),
    kind: codexItemKind(item, fileChanges),
    finished,
    sides,
    statusTitle: codexStatusItemTitle(type),
    detail: codexStatusDetail(item),
    images: codexItemImages(item),
    fileChanges,
    aggregatedOutput: pickString(item, CODEX_ITEM_FIELD.AggregatedOutput),
  }
}

/** One kind's specification, read from the facts alone. */
/**
 * The kinds Codex reads from its own facts rather than from the shared table.
 *
 * Empty, and it stays empty. A kind Codex produces states its whole specification in
 * {@link CODEX_TOOL_READERS} -- the request and the result together -- so an entry
 * here would be a second place one Codex request is built, and the two would drift.
 * An entry belongs here only for a kind whose specification reader Codex does not state.
 */
const CODEX_TOOL_REQUEST_OVERRIDES: ToolRequestOverrides<CodexToolFacts> = {}

/**
 * A kind Codex states no item for: the shared DECLARED request, and no result.
 *
 * The item is the argument record, so `DEFAULT_TOOL_REQUESTS` reads the keys it reads
 * for every other provider. The entry exists for TOTALITY: the table must answer for
 * every `ToolKind`, and a kind with no entry at all would have to fall through to a
 * `{ args }` payload -- which the renderers read without a guard, so `request.path`
 * or `request.changes[0]` throws the whole message into the ErrorBoundary.
 */
function codexDeclaredOnly<P extends ToolKind>(kind: P): ToolCallSpecReader<CodexToolFacts, P> {
  return (facts): ToolCallSpecVariant<P> => ({ kind, request: toolRequestFor(kind, facts.item, facts, CODEX_TOOL_REQUEST_OVERRIDES) })
}

/**
 * Check every reader against its declared kind's request and result.
 *
 * A switch narrows a value without narrowing its generic type parameter.
 * A union-returning branch could therefore omit the required request or return another kind's shape.
 * This mapped table rejects both mistakes. A missing kind is a compile error.
 *
 * Each kind retains its own lifecycle decisions.
 * Commands carry output streams. File changes report whether their changes applied.
 * Review markers complete when their items arrive.
 *
 * Every entry must declare its return type.
 * An inferred arrow return loses object-literal freshness before the contextual signature checks it.
 * Its excess properties can then escape validation and reach fields that no renderer reads.
 * An explicit return type keeps each literal in a checked position.
 * For the same reason, do not return an intermediate variable without its own type annotation.
 */
export const CODEX_TOOL_READERS: ToolCallSpecReaderTable<CodexToolFacts> = {
  execute: (facts): ToolCallSpecVariant<'execute'> => {
    const command = codexUnwrapCommand(pickString(facts.item, 'command'))
    const cwd = pickString(facts.item, 'cwd') || undefined
    const processId = pickString(facts.item, 'processId') || undefined
    const actions = codexCommandActionsFromItem(facts.item)
    const request: ExecuteRequest = {
      command,
      ...(cwd !== undefined ? { cwd } : {}),
      ...(processId !== undefined ? { processId } : {}),
      ...(actions.length > 0 ? { actions } : {}),
    }
    // Only a call that ENDED has an output stream to state. Codex sends the exit code
    // and the aggregated output on the same item, so the result is the item itself.
    const source = facts.finished ? codexCommandFromItem(facts.item) : null
    return source
      ? { kind: 'execute', request, result: { commands: [source], unresolvedTerminals: [] } }
      : { kind: 'execute', request }
  },
  // The four file kinds declare the same request and the same result, and one item
  // builds all four the same way -- but each entry states its OWN kind, because a
  // shared generic would put the pair beyond the checker again.
  edit: (facts): ToolCallSpecVariant<'edit'> => ({ kind: 'edit', ...codexFileChangeParts(facts) }),
  write: (facts): ToolCallSpecVariant<'write'> => ({ kind: 'write', ...codexFileChangeParts(facts) }),
  delete: (facts): ToolCallSpecVariant<'delete'> => ({ kind: 'delete', ...codexFileChangeParts(facts) }),
  move: (facts): ToolCallSpecVariant<'move'> => ({ kind: 'move', ...codexFileChangeParts(facts) }),
  image: (facts): ToolCallSpecVariant<'image'> => {
    const failure = pickString(pickObject(facts.item, 'failure'), 'type')
    // The prompt the model actually rendered from often differs from the one the
    // reader asked for, so it belongs on the REQUEST: the result lands only when
    // the picture does, and the row states nothing else while it generates.
    const prompt = pickString(facts.item, 'revisedPrompt') || undefined
    const request: ImageRequest = { ...(prompt !== undefined ? { prompt } : {}) }
    if (failure)
      return { kind: 'image', request, title: `Generate image ${failure}`, result: failedResult(failure), statusOverride: 'failed' }
    if (facts.images.length > 0)
      return { kind: 'image', request, title: 'Generate image', images: facts.images, result: { ...(prompt !== undefined ? { revisedPrompt: prompt } : {}) } }
    return { kind: 'image', request, title: 'Generate image' }
  },
  read: (facts): ToolCallSpecVariant<'read'> => {
    // An `imageView` sends a `file:` URI where every other item sends a plain path. A
    // URI the parser refuses keeps its raw text, which states more than a blank path.
    const raw = pickString(facts.item, 'path')
    const path = codexItemPath(raw) ?? raw
    return facts.finished && facts.images.length > 0
      ? { kind: 'read', request: { path }, images: facts.images, result: { lines: null, fallbackContent: '' } }
      : { kind: 'read', request: { path } }
  },
  fetch: (facts): ToolCallSpecVariant<'fetch'> => {
    // `codexItemKind` answers `fetch` for exactly the `openPage` action, reading the
    // same item through the same function -- so the action below is always that page.
    // Its url is empty only for a frame the classifier already hid, which is why the
    // fall-back states the empty string rather than a second reading of the item.
    const action = codexWebSearchActionFromItem(facts.item)
    const url = action?.type === 'openPage' ? action.url : ''
    return facts.finished
      ? { kind: 'fetch', request: { url }, title: url, result: { result: '' } }
      : { kind: 'fetch', request: { url }, title: url }
  },
  web_search: (facts): ToolCallSpecVariant<'web_search'> => {
    const action = codexWebSearchActionFromItem(facts.item)
    // A finished search states its (empty) result: the item carries no links of its
    // own, and the row keeps the queries an expand control reveals.
    const result: WebSearchResult | undefined = facts.finished ? { links: [], summary: '' } : undefined
    if (action?.type === 'findInPage')
      return { kind: 'web_search', request: { query: action.pattern, inPage: { pattern: action.pattern, ...(action.url !== undefined ? { url: action.url } : {}) } }, ...(result !== undefined ? { result } : {}) }
    if (action?.type === 'search')
      return { kind: 'web_search', request: { query: action.query, queries: action.queries }, ...(result !== undefined ? { result } : {}) }
    // An action with no fields states what it does rather than nothing.
    const query = action?.type === 'other' ? action.query : ''
    return { kind: 'web_search', request: { query }, title: query || 'Searching the web', ...(result !== undefined ? { result } : {}) }
  },
  agent: (facts): ToolCallSpecVariant<'agent'> => {
    const counterpart = codexAgentCounterpart(facts.item, facts.finished ? facts.sides.request : facts.sides.result, facts.finished ? 'request' : 'result')
    const resolved = resolveCodexAgentItem(facts.item, counterpart)
    const request = codexAgentRequest(resolved)
    return {
      kind: 'agent',
      request,
      title: request.description,
      // The call's completion does not establish that any child agent completed, so
      // the runs are read from the item and each states its own outcome.
      ...(facts.finished ? { result: { agents: codexAgentResults(resolved) } } : {}),
      ...(facts.status === 'interrupted' ? { statusOverride: 'cancelled' as const } : {}),
    }
  },
  mcp: codexMcpSpec,
  wait: (facts): ToolCallSpecVariant<'wait'> => {
    const durationMs = pickNumber(facts.item, 'durationMs', undefined)
    return {
      kind: 'wait',
      request: { ...(durationMs !== undefined ? { durationMs } : {}) },
      ...(facts.statusTitle !== undefined ? { title: facts.statusTitle } : {}),
      result: proseResult(facts.detail),
    }
  },
  switch_mode: (facts): ToolCallSpecVariant<'switch_mode'> => ({
    kind: 'switch_mode',
    // No `mode`: `switchModeRenderer` titles the row from `request.mode` first,
    // so stating one here would draw the bare word "review" for BOTH the entry
    // and the exit marker and hide the sentence composed below.
    request: {},
    ...(facts.statusTitle !== undefined ? { title: facts.statusTitle } : {}),
    result: proseResult(facts.detail),
  }),
  skill: codexHookPromptSpec,
  /*
   * Typed status items keep their own title and detail.
   * The Worker also persists item types from later releases.
   * Each becomes a neutral other-kind call with its own sentence or wire type.
   * Classification and extraction must agree so the measured row contains visible content.
   */
  other: (facts): ToolCallSpecVariant<'other'> => {
    const title = facts.statusTitle ?? (facts.type ? humanizeWireWord(facts.type) : undefined)
    return {
      kind: 'other',
      ...(title !== undefined ? { title } : {}),
      request: { args: facts.item },
      result: { content: facts.detail ? [{ type: 'text', text: facts.detail }] : [] },
    }
  },
  // The kinds no Codex ITEM takes. Each states the shared declared request, so the
  // table stays total and no kind can reach a payload that states nothing.
  //
  // `todo` is here for a different reason than the rest: Codex DOES draw a to-do row,
  // and `codexTurnPlanRow` builds it from a notification that carries no item. See
  // the note there.
  unspecified: codexDeclaredOnly('unspecified'),
  agents: codexDeclaredOnly('agents'),
  chart: codexDeclaredOnly('chart'),
  glob: codexDeclaredOnly('glob'),
  grep: codexDeclaredOnly('grep'),
  list: codexDeclaredOnly('list'),
  memory: codexDeclaredOnly('memory'),
  message: codexDeclaredOnly('message'),
  question: codexDeclaredOnly('question'),
  report: codexDeclaredOnly('report'),
  search: codexDeclaredOnly('search'),
  task: codexDeclaredOnly('task'),
  think: codexDeclaredOnly('think'),
  todo: codexDeclaredOnly('todo'),
  trigger: codexDeclaredOnly('trigger'),
}

/**
 * One item's specification at one kind.
 *
 * Generic over the kind, so the kind and its specification stay one correlated
 * pair. That is what removes the assertion the old `switch` needed at every branch,
 * and the assertion ban in `eslint.config.ts` refuses exactly that assertion.
 */
export function codexSpecFor<K extends ToolKind>(facts: CodexToolFacts, kind: K): { [P in K]: ToolCallSpecVariant<P> }[K] {
  return readToolCallSpec(CODEX_TOOL_READERS, kind, facts)
}

/**
 * The file-change card's parts: the change the item states, and whether it landed.
 *
 * The four file kinds share this, and it states no `kind` of its own -- each entry of
 * the table spells its own, which is what keeps every pair checked.
 */
function codexFileChangeParts(facts: CodexToolFacts): { request: FileChangeRequest, title?: string, result?: FileChangeResult | ToolFailureResult } {
  const entries = facts.fileChanges
  const changes = codexRequestedChanges(entries)
  const request: FileChangeRequest = { changes }
  // One change identifies its file; several state their count. NONE states nothing:
  // Codex sends the item before its `changes` list, so a running edit worded itself
  // "0 files" -- which reads as an edit that touched nothing rather than one whose
  // file list has not arrived. With no title the row falls back to the kind's own
  // word, which is what every other provider's pending edit shows.
  //
  // The REQUEST is what words it, at every state. A completed change whose entry
  // carried no readable body answers an empty landed list, and the shared title then
  // reads this one -- which is the only place the file name survives.
  const title = changes.length === 1
    ? changes[0]?.filePath
    : changes.length > 1 ? pluralize(changes.length, 'file') : undefined
  const headed = title !== undefined ? { title } : {}
  // Only a COMPLETED change landed. A running one states what it asks for, and
  // a failed or declined one never happened -- drawing its diff as a result
  // would report an edit the file never received.
  if (facts.status === CODEX_STATUS.COMPLETED)
    return { request, ...headed, result: { changes: fileEditDiffsFromChanges(entries) } }
  // `finished`, not the item's own word, for the reason `execute` gives: LeapMux's
  // reading of how the turn ended wins, and a retained row leaves the last
  // `inProgress` frame stored. Keying on the wire word alone dropped the
  // aggregated output of every retained change -- the apply-patch error, which is
  // the one thing that states WHY the change did not land.
  if (!facts.finished)
    return { request, ...headed }
  const failure = facts.aggregatedOutput.trim()
  return failure ? { request, ...headed, result: failedResult(failure) } : { request, ...headed }
}

/**
 * Read a Model Context Protocol request and result from these item variants:
 * - A server call.
 * - A dynamic call.
 * - An output item.
 */
function codexMcpSpec(facts: CodexToolFacts): ToolCallSpecVariant<'mcp'> {
  if (facts.type === CODEX_ITEM.FunctionCallOutput) {
    const namespace = pickString(facts.item, 'namespace')
    const callId = pickString(facts.item, 'callId') || pickString(facts.item, 'id')
    const output = pickString(facts.item, 'output')
    return {
      kind: 'mcp',
      title: mcpToolCallDisplayName({ server: namespace, tool: callId || 'Function call' }),
      request: { server: namespace, tool: callId || 'Function call', args: {} },
      result: { content: output ? [{ type: 'text', text: output }] : [] },
    }
  }
  const source = codexMcpFromItem(facts.item)
  if (!source)
    return { kind: 'mcp', request: { server: '', tool: pickString(facts.item, 'id'), args: {} } }
  return {
    kind: 'mcp',
    request: { server: source.server, tool: source.tool, args: safeJson(source.argsJson ?? '') },
    result: {
      content: source.content,
      ...(source.structuredJson !== undefined ? { structuredJson: source.structuredJson } : {}),
      ...(source.error !== undefined ? { error: source.error } : {}),
      ...(source.durationMs !== undefined ? { durationMs: source.durationMs } : {}),
    },
    ...(source.failed ? { statusOverride: 'failed' as const } : {}),
  }
}

/** The arguments a pretty-printed JSON string states, or none when it states none. */
function safeJson(text: string): Record<string, unknown> {
  try {
    const parsed = JSON.parse(text) as unknown
    return isObject(parsed) ? parsed : {}
  }
  catch {
    return {}
  }
}

/**
 * Read one hook's instructions from its native fragments.
 *
 * Codex sends {id, fragments: [{hookRunId, text}]}.
 * Read the fragments in wire order. Join their text and render it as Markdown.
 * This gives the row a body and a title instead of an empty measured row.
 */
function codexHookPromptSpec(facts: CodexToolFacts): ToolCallSpecVariant<'skill'> {
  const raw: unknown[] = Array.isArray(facts.item.fragments) ? facts.item.fragments : []
  const fragments = raw
    .map(fragment => typeof fragment === 'string' ? fragment : pickString(isObject(fragment) ? fragment : undefined, 'text'))
    .filter(Boolean)
  const text = fragments.join('\n\n') || pickString(facts.item, 'text')
  const first = raw[0]
  const runId = pickString(isObject(first) ? first : undefined, 'hookRunId')
  return {
    kind: 'skill',
    title: runId ? `Hook prompt (${runId})` : 'Hook prompt',
    request: { ...(runId ? { name: runId } : {}) },
    result: proseResult(text, 'markdown'),
  }
}

/**
 * One Codex item as the kind-discriminated call.
 *
 * The item carries both the arguments and result data. The sides differ only in
 * status -- so one set of facts builds the pair, and `finished` decides which half the
 * row states.
 */
function codexToolCall(facts: CodexToolFacts, lifecycle: ToolCallLifecycleFacts): ToolCall {
  const spec = codexSpecFor(facts, facts.kind)
  const label = codexItemLabel(facts)
  return createToolCall(
    {
      id: pickString(facts.item, 'id'),
      name: facts.type,
      lifecycle,
    },
    { ...spec, ...(label !== undefined ? { label } : {}) },
  )
}

/** The provider's own display word, when the kind's word states less. */
function codexItemLabel(facts: CodexToolFacts): string | undefined {
  switch (facts.type) {
    case CODEX_ITEM.CommandExecution: return 'Command Execution'
    case CODEX_ITEM.FileChange: return 'File Change'
    case CODEX_ITEM.ImageView: return 'View image'
    case CODEX_ITEM.ImageGeneration: return 'ImageGeneration'
    case CODEX_ITEM.WebSearch:
      // From the kind, not a third parse of the same action: `codexItemKind` answers
      // `fetch` for exactly the `openPage` case this label separates.
      return facts.kind === 'fetch' ? 'WebFetch' : 'WebSearch'
    case CODEX_ITEM.HookPrompt: return 'Hook'
    case CODEX_ITEM.FunctionCallOutput: return 'Function call output'
    default:
      return facts.statusTitle ?? (facts.kind === 'other' && facts.type ? humanizeWireWord(facts.type) : undefined)
  }
}

/**
 * One Codex tool span as a merged call.
 *
 * An item that states NO status of its own -- `imageView` is the live case -- cannot
 * say where it sits in its span, so the resolver's role answers instead. Both sides
 * of such a span carry the same bytes, and without this they both read as the
 * request: the result then drew a second header and no picture.
 */
function codexToolSpanRow(parsed: ParsedMessageContent, sides: RowExtractionInput['span'], completion?: MessageCompletion): ChatRow | null {
  const payload = parsed.parentObject
  if (payload) {
    const execution = codexRawExecCall(payload, sides, completion)
    if (execution)
      return toolCallRow(execution.call, execution.role, sides.visibleRows)
  }
  // `turnPlan` dispatches off the notification METHOD rather than an item type, so it
  // answers before the item table.
  if (payload && pickString(payload, 'method') === CODEX_METHOD.TurnPlanUpdated)
    return codexTurnPlanRow(payload)
  const item = extractItem(parsed.parentObject)
  if (!item)
    return null
  const statusWord = pickString(item, 'status')
  const atomicResult = isCodexAtomicResultItem(pickString(item, 'type'))
  const role: ToolSpanRowRole = atomicResult || sides.role === 'result'
    ? 'result'
    : sides.role === 'request'
      ? 'request'
      : (isCodexFinishedStatus(statusWord) || !!parsed.completion ? 'result' : 'request')
  const rowFinal = role === 'result' || isCodexFinishedStatus(statusWord) || !!parsed.completion
  // LeapMux's own reading of how the turn ended wins over the item word, as
  // `ToolCallBase.status` states for every provider. A turn the reader stopped
  // leaves the last `inProgress` frame stored, so the item still reads as running
  // and the replayed row would spin for the life of the transcript.
  const outcome = retainedOutcome(completion)
  // A span whose CLOSING row states no status of its own -- `imageView` is the live
  // case, final by `completedAtMs` alone -- still ENDED, and the resolver said so
  // through the role above. Its status word defaults to in-progress, and a call that
  // pairs that word with a finished span's pictures is a draft the validating
  // builder refuses: the row degraded to the generic card and lost the picture.
  // The role's answer states the outcome the frame's own words cannot.
  const candidateResultItem = sides.result ? extractItem(sides.result.parentObject) : null
  // A result frame belongs to this call only when both identity fields match. A
  // corrupt span must not mark this call as answered or project another call's data.
  const matchingResultItem = candidateResultItem
    && pickString(candidateResultItem, 'id') === pickString(item, 'id')
    && pickString(candidateResultItem, 'type') === pickString(item, 'type')
    ? candidateResultItem
    : null
  // A closing fileChange is the authoritative full item: it carries the original
  // change list and states which changes landed. Other Codex item kinds keep their
  // existing per-frame projections even though the lifecycle sees their result.
  const projectedResultItem = item.type === CODEX_ITEM.FileChange ? matchingResultItem : null
  // Read every resolved span side.
  // A request row includes its landed result, so its prompt uses the same compact expansion control.
  const answered = atomicResult || sides.role === 'result' || isCodexFinishedStatus(statusWord) || !!matchingResultItem
  // A landed result finishes the call even when the request frame still says inProgress.
  // A finished result must not use an in-progress envelope. The validating builder would discard its typed card.
  // The shared lifecycle derivation decides the precedence of these facts:
  // - The item status.
  // - The provider's interruption.
  // - The retained outcome.
  // - The landed result.
  const lifecycle: ToolCallLifecycleFacts = {
    frameStatus: codexFrameStatus(statusWord),
    providerOutcome: statusWord === 'interrupted' ? 'interrupted' : null,
    retainedOutcome: outcome,
    rowFinal,
    resultFrameLanded: answered,
  }
  const call = codexToolCall(codexToolFacts(projectedResultItem ?? item, rowFinal || !!projectedResultItem, sides), lifecycle)
  return toolCallRow(call, role, sides.visibleRows)
}

/**
 * The item's own status word in the shared vocabulary.
 *
 * The ONE conversion Codex needs: `inProgress` is a call that still runs, and a raw
 * `interrupted` is not a status at all -- it is the provider's own conclusion, which
 * the lifecycle carries as its outcome fact while the frame states none.
 */
function codexFrameStatus(statusWord: string): ToolCallStatus {
  if (statusWord === CODEX_STATUS.IN_PROGRESS)
    return 'in_progress'
  return statusWord === 'interrupted' ? 'unstated' : toolCallStatus(statusWord)
}
