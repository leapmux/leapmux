import type { JSX } from 'solid-js'
import type { AgentPromptIR } from './ir/divider'
import type { ChatRowIR } from './ir/row'
import type { MessageCategory } from './messageClassification'
import type { RenderContext } from './messageRenderers'
import type { ChatRowExtraction } from './rowExtraction'
import type { ResolvedMessageContent } from './rowExtractionTypes'
import type { ToolSpanSides } from '~/components/chat/rowExtractionTypes'
import type { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { TodoItem } from '~/models/todo'
import CircleAlert from 'lucide-solid/icons/circle-alert'
import MessageSquare from 'lucide-solid/icons/message-square'
import { createMemo, untrack } from 'solid-js'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { assertNever } from '~/lib/assertNever'
import { cachedInnerHtml } from '~/lib/htmlFragmentCache'
import { isObject } from '~/lib/jsonPick'
import { createLogger } from '~/lib/logger'
import { completionMarker, messageCompletionFromProto } from './assembledMessage'
import { COLLAPSED_RESULT_ROWS, hasMoreLinesThan } from './ir/collapse'
import { toolOutcomeLabel } from './ir/toolOutcomeLabel'
import { fixedCacheKey } from './messageRenderCache'
import {
  MarkdownText,
  PlanExecutionMessage,
  renderControlResponseRow,
  renderMarkdownForContext,
  ThinkingMessage,
  UnrecognizedMessage,
  UserContentMessage,
  useSharedExpandedState,
} from './messageRenderers'
import { MESSAGE_UI_KEY } from './messageUiKeys'
import { flattenNotificationEntries } from './notificationEntries'
import { renderNotificationBlocks } from './notificationRenderers'
import { resolveMessageForRendering } from './providers/registry'
import { ResultDivider } from './resultDividerRenderers'
import { ToolMessage } from './results/ToolMessage'
import { ToolStatusHeader } from './results/ToolStatusHeader'
import { extractChatRow } from './rowExtraction'
import { toolOutcomeNote } from './toolOutcome'
import { toolResultCollapsed, toolResultContent } from './toolStyles.css'
import { MarkdownPlanLayout } from './widgets/MarkdownPlanLayout'
import { ToolUseLayout } from './widgets/ToolUseLayout'

/**
 * Draw one row from the shared IR.
 *
 * Layer 3 of the render pipeline, and the ONE place a row kind becomes markup. It
 * branches on the kind and on nothing else -- no provider, no tool name, no wire
 * shape -- because layer 1 already answered those.
 *
 * EXHAUSTIVE: a new row kind is a compile error here rather than a row that silently
 * draws nothing.
 */
export function renderRowContent(
  row: ChatRowIR,
  context: RenderContext | undefined,
): JSX.Element {
  switch (row.kind) {
    case 'tool':
      return (
        <ToolMessage
          row={row}
          {...(context !== undefined ? { context } : {})}
          {...(context?.toolProgress !== undefined ? { progress: context.toolProgress } : {})}
        />
      )
    case 'notification':
      return renderNotificationBlocks(flattenNotificationEntries(row.thread.entries))
    case 'divider':
      return <ResultDivider model={row.divider} />
    case 'assistant-text':
      return <MarkdownText text={row.text} {...(context !== undefined ? { context } : {})} />
    case 'assistant-thinking':
      return <ThinkingMessage text={row.text} {...(context !== undefined ? { context } : {})} />
    case 'assistant-plan':
      return <MarkdownPlanLayout toolName="Plan" title="Proposed Plan" planText={row.text} {...(context !== undefined ? { context } : {})} />
    case 'user':
      return <UserContentMessage parsed={userPayload(row)} {...(context !== undefined ? { context } : {})} />
    case 'agent-prompt':
      return <AgentPromptView prompt={row.prompt} {...(context !== undefined ? { context } : {})} />
    case 'plan-execution':
      return <PlanExecutionMessage text={row.text} {...(context !== undefined ? { context } : {})} />
    case 'compact-summary':
      return <MarkdownText text={row.summary} {...(context !== undefined ? { context } : {})} />
    case 'control-response':
      return renderControlResponseRow(row.display, context)
    case 'hidden':
      return null
    case 'unrecognized':
      return <UnrecognizedMessage payload={row.payload} {...(context !== undefined ? { context } : {})} />
    default:
      return assertNever(row)
  }
}

/**
 * The flat `{content, attachments}` shape `UserContentMessage` reads.
 *
 * LeapMux writes every user row in that shape, so the IR carries the two fields
 * apart and this rebuilds the object the shared card takes rather than making the
 * card learn a second shape.
 */
function userPayload(row: Extract<ChatRowIR, { kind: 'user' }>): Record<string, unknown> {
  return {
    content: row.text,
    attachments: row.attachments.map(item => ({ filename: item.filename, mime_type: item.mimeType })),
  }
}

/**
 * The subagent prompt card, shared by every provider that sends one.
 *
 * Promoted from Claude's local copy. Three surfaces draw this row -- a provider's own
 * prompt row, Claude's `agent_prompt`, and a prompt delivered into a child transcript
 * -- and the card must read the same for all three.
 */
export function AgentPromptView(props: { prompt: AgentPromptIR, context?: RenderContext }): JSX.Element {
  // Key from the shared classification mapper (context.expandUiKey) so it matches
  // the estimator's pre-mount assumption; the literal is the context-less fallback.
  // untrack: the key is stable for a row (kind+provider don't change), so read it
  // once -- mirrors ThinkingBubble's `untrack(() => props.stateKey)`.
  const stateKey = untrack(() => props.context?.expandUiKey ?? MESSAGE_UI_KEY.AGENT_PROMPT)
  const [expanded, setExpanded] = useSharedExpandedState(() => props.context, stateKey)
  const text = () => props.prompt.prompt
  const isCollapsed = () => !expanded() && hasMoreLinesThan(text(), COLLAPSED_RESULT_ROWS)
  const html = createMemo(() => renderMarkdownForContext(text(), props.context))
  // The title states WHAT the subagent was asked to do when the provider said so.
  // `Prompt` alone is what every provider fell back to, and it named nothing.
  const title = () => [props.prompt.description, props.prompt.agentType].filter(Boolean).join(' · ') || 'Prompt'

  return (
    <ToolUseLayout
      icon={MessageSquare}
      toolName="Prompt"
      title={title()}
      expanded={expanded()}
      onToggleExpand={() => setExpanded(v => !v)}
      {...(props.context !== undefined ? { context: props.context } : {})}
    >
      <div
        class={`${toolResultContent}${isCollapsed() ? ` ${toolResultCollapsed}` : ''}`}
        ref={props.prompt.promptFormat === 'pre' ? undefined : cachedInnerHtml(html)}
      >
        {props.prompt.promptFormat === 'pre' ? text() : undefined}
      </div>
    </ToolUseLayout>
  )
}

const logger = createLogger('rowRenderers')

/**
 * The part of a {@link RenderContext} that reading a row needs.
 *
 * Structural rather than the whole context, because `MessageBubble` builds this
 * much BEFORE it builds the render context: the context's `hasOuterToolbar`
 * getter reads the toolbar, the toolbar reads the row, and the row would then
 * read a context that does not exist yet.
 */
export type RowExtractionContext = Pick<RenderContext, 'renderCache' | 'sources' | 'spanType'>

/**
 * The three sides of a row's tool span, resolved once for an extraction.
 *
 * Resolved ONCE per row, so a plugin never reaches back into `context.sources` for a
 * side the caller already had -- each read there is a fresh resolution outside the
 * memo that produced this input.
 */
function rowSpanSides(context: RowExtractionContext | undefined): ToolSpanSides {
  const current = context?.sources?.current()
  return {
    current,
    request: context?.sources?.request(),
    result: context?.sources?.result(),
    role: context?.sources?.role() ?? 'other',
  }
}

/** The row IR one row revision extracted: the row cache's one fixed entry. */
const ROW_CACHE_KEY = fixedCacheKey<CachedRowEntry>('ir.row')

/**
 * One cached row, beside what its extraction read from OUTSIDE the messages.
 *
 * The cache key folds the row's own content version and both span siblings'
 * revisions (`renderCacheKeyForEntry`), which covers every input the extraction takes
 * from the messages. The to-do store is the one input that is not a message, so the
 * entry carries the answers it got and the next read checks them again.
 */
interface CachedRowEntry {
  extraction: ChatRowExtraction
  /**
   * The snapshot of each to-do the extraction ASKED the store for, by task id.
   * `undefined` states the store held none at extraction time, which the one-way
   * rule below needs beside a present snapshot: a task that ARRIVES repairs the row.
   */
  todos?: ReadonlyMap<string, TodoItem | undefined>
}

/**
 * A copied snapshot of one to-do, for comparison alone.
 *
 * A COPY, because the store's row is live: the fields a comparison reads must be the
 * answers the extraction SAW, not whatever the store holds by the time the cached
 * entry is checked. Every field is a primitive, so the spread is the whole copy.
 */
function todoSnapshot(item: TodoItem): TodoItem {
  return { ...item }
}

/**
 * Whether a to-do the cached extraction read still answers what it answered then.
 *
 * Field by field, over the keys BOTH copies state, so a field one side omits and the
 * other fills is a difference. A new field on `TodoItem` joins the walk the day it
 * is added -- the spread copies it and the key union walks it -- with no list to
 * keep in step.
 */
function todoDiffers(before: TodoItem, after: TodoItem): boolean {
  for (const key of new Set([...Object.keys(before), ...Object.keys(after)] as Array<keyof TodoItem>)) {
    if (before[key] !== after[key])
      return true
  }
  return false
}

/**
 * Whether every to-do the cached extraction read still answers the same.
 *
 * The check runs ONE WAY: a to-do that is present and DIFFERENT invalidates the entry,
 * and one that left the store never does. Both directions look like a mismatch, and
 * treating them alike made the row revert -- a `TaskUpdate` whose subject the store
 * supplied went back to `Task #42`, lost its note and un-checked its box, every time
 * the task left. It leaves routinely: `TodoWrite` replaces the whole list that the
 * `Task*` family shares, a context clear empties it, and the cap evicts FINISHED rows
 * first, which is exactly what scrollback holds. A row that was right must not become
 * wrong, and an absent to-do tells us nothing that the cached answer does not.
 */
function todoReadsHold(
  todos: ReadonlyMap<string, TodoItem | undefined> | undefined,
  lookup: ((taskId: string) => TodoItem | undefined) | undefined,
): boolean {
  if (!todos)
    return true
  for (const [taskId, snapshot] of todos) {
    const current = lookup?.(taskId)
    // One way: a to-do that LEFT the store never invalidates the row (see the
    // paragraph above). A to-do that ARRIVED does, and so does one that changed.
    if (current === undefined)
      continue
    if (snapshot === undefined || todoDiffers(snapshot, current))
      return false
  }
  return true
}

/**
 * One row's IR, built at most once for each revision of the row.
 *
 * A stable object across renders is what keeps the row from re-creating its DOM
 * whenever an unrelated signal moves -- and it is what lets `MessageBubble` derive
 * its toolbar from the SAME row the transcript drew, rather than from a second walk
 * of the same bytes.
 *
 * The to-do store is the one extraction input no revision key can carry, because it
 * is not a message: Claude's `TaskUpdate` states a task id and a status, and the
 * subject, the active form and the description come from the store. A row drawn
 * before its task reached the store wrote `Task #4` into the cached IR, and nothing
 * released it -- so the entry records what it read and this re-checks it. The
 * re-check is also what SUBSCRIBES the row to those to-dos, because the store read
 * now happens on every pass rather than on the first one alone.
 *
 * A repair CHANGES the row's height, and `heightKeyForEntry` folds no to-do state, so
 * the committed height is the pre-repair one. The subject wraps (the full-variant list
 * wraps its labels) and the store's `description` arrives as a whole markdown note that
 * was not there before. A mounted row re-measures itself through its ResizeObserver, so
 * the reader sees the rows below it shift once; a row inside the window that is not
 * mounted keeps the short height until it mounts. Folding the to-do state into the
 * height key means `ChatView` tracking the store for every row, which is the change
 * this needs and does not make.
 */
export function cachedChatRow(
  context: RowExtractionContext | undefined,
  agentProvider: AgentProvider | undefined,
  parsed: ResolvedMessageContent,
  category: MessageCategory,
  completion: MessageCompletion | undefined,
): ChatRowExtraction {
  const cache = context?.renderCache
  const lookup = context?.sources?.todo
  const cached = cache?.get(ROW_CACHE_KEY)
  if (cached && todoReadsHold(cached.todos, lookup))
    return cached.extraction
  const todos = new Map<string, TodoItem | undefined>()
  const extraction = extractChatRow(agentProvider, parsed, category, {
    sides: rowSpanSides(context),
    ...(completion !== undefined ? { completion } : {}),
    ...(context?.spanType !== undefined ? { spanType: context.spanType } : {}),
    ...(lookup !== undefined
      ? {
          todoById: (taskId: string): TodoItem | undefined => {
            const item = lookup(taskId)
            todos.set(taskId, item ? todoSnapshot(item) : undefined)
            return item
          },
        }
      : {}),
  })
  cache?.set(ROW_CACHE_KEY, todos.size > 0 ? { extraction, todos } : { extraction })
  return extraction
}

/**
 * A minimal parsed message for a caller that supplies no resolved sources.
 *
 * Only an isolated render reaches it -- a test or a preview that passes a payload and
 * no context. Every mounted row carries `sources.current()`, which is the resolved
 * message including its supplemental data.
 */
function parsedMessageOf(parsed: unknown): ParsedMessageContent {
  const parentObject = isObject(parsed) ? parsed : undefined
  return { wrapper: null, topLevel: parentObject ?? null, parentObject, rawText: '', supplementalContent: undefined, messageMetadata: undefined }
}

/**
 * Overlay `completionHeader: true` on a render context without freezing its
 * reactive getters.
 *
 * Every own member of the base is re-stated on the overlay as a FORWARDING
 * descriptor -- a getter reads through to the base on every access, a value is
 * copied once -- and each stays OWN and enumerable. A plain `{...context, …}`
 * overlay evaluates every getter at one pass, so a row that streamed drew the
 * output it held when the interrupted header was built; a prototype overlay keeps
 * the getters live but leaves them off any later spread. This does both.
 */
function withCompletionHeader(context: RenderContext | undefined): RenderContext {
  const overlay = { completionHeader: true } as RenderContext
  if (context === undefined)
    return overlay
  for (const [key, descriptor] of Object.entries(Object.getOwnPropertyDescriptors(context))) {
    if (key === 'completionHeader')
      continue
    if (descriptor.get !== undefined || descriptor.set !== undefined) {
      Object.defineProperty(overlay, key, {
        ...(descriptor.get !== undefined ? { get: descriptor.get } : {}),
        ...(descriptor.set !== undefined ? { set: descriptor.set } : {}),
        enumerable: true,
        configurable: true,
      })
    }
    else {
      Object.defineProperty(overlay, key, { value: descriptor.value, writable: true, enumerable: true, configurable: true })
    }
  }
  return overlay
}

/**
 * Draw an extracted row, with the completion chrome every row shares.
 *
 * A row the provider could not read at all draws the shared unrecognized card, so the
 * reader still gets the frame. The interruption and failure headers wrap the drawn row
 * exactly as they wrap a legacy one, because they are LeapMux's own statement about
 * the row rather than any provider's.
 *
 * The completion comes from the EXTRACTION, which read LeapMux's own column and the
 * assembled envelope's own statement in that order. This function derived it from two
 * of its arguments instead, and the scroll rail derived it from one -- so an
 * interrupted thought whose worker column was unset carried its notice in the
 * transcript and lost it under the dot that jumps there.
 */
export function renderExtractedRow(
  extraction: ChatRowExtraction,
  context: RenderContext | undefined,
): JSX.Element {
  const row = extraction.kind === 'row' ? extraction.row : null
  // The one row kind the chrome below asks about. Held as its own narrowed binding so
  // the two questions -- does this row take a completion header, and does the outcome
  // note replace its body -- read the role off the same object.
  const toolRow = row?.kind === 'tool' ? row : null
  const completion = extraction.completion
  const toolCompletion = toolRow !== null && (completion === 'interrupted' || completion === 'error')
  // The outcome note is LeapMux's own statement about a tool row, so it is drawn here
  // rather than by any provider.
  const note = toolOutcomeNote(context?.sources?.current()?.messageMetadata)
  // The note says the agent sent NO result for this call, and LeapMux concluded the
  // outcome itself. The RESULT row then draws no body at all: an empty body reads as
  // "the tool returned nothing", which asserts something the agent never reported.
  // ONE rule for every provider -- ZCode alone used to apply it, inside its own
  // renderer, so the same row on another provider drew "[no output]" beside the note.
  //
  // A suppression FLAG rather than an early return: returning here also skipped the
  // interruption header and the completion marker below, so a row LeapMux marked
  // interrupted drew one bare sentence with nothing saying which tool it belonged to
  // or that the turn had been stopped.
  const bodySuppressed = note !== null && toolRow?.role === 'result'
  const rowContext = toolCompletion
    ? withCompletionHeader(context)
    : context
  // A frame nobody could read still reaches the reader, in the card that says so.
  // The card states WHICH of the two happened: "LeapMux could not render this row"
  // for an extraction that threw, and "LeapMux has no display for this row" for one
  // no reader claimed. Folding them together blamed the provider for a defect in
  // LeapMux, and the `unrecognized` row IR carries no such distinction on purpose --
  // a provider that returns one is stating the second, never the first.
  //
  // Built under the `if` rather than as a suppressed value, because a JSX expression
  // CALLS its component: an eagerly built body would run the tool renderer for a row
  // whose body the outcome note replaces.
  let drawn: JSX.Element = null
  if (!bodySuppressed) {
    drawn = extraction.kind === 'row'
      ? renderRowContent(extraction.row, rowContext)
      : <UnrecognizedMessage payload={extraction.payload} renderFailed={extraction.kind === 'failed'} {...(context !== undefined ? { context } : {})} />
  }
  const withNote = note === null
    ? drawn
    : (
        <>
          {drawn}
          <div role="note">{note}</div>
        </>
      )
  if (toolCompletion) {
    return (
      <ToolStatusHeader icon={CircleAlert} title={toolOutcomeLabel(completion === 'interrupted' ? 'interrupted' : 'failed')} dataToolMessage>
        {withNote}
      </ToolStatusHeader>
    )
  }
  const marker = completionMarker(completion)
  return marker
    ? (
        <>
          {withNote}
          <div role="note">{marker}</div>
        </>
      )
    : withNote
}

/**
 * Render a message's content.
 *
 * ONE dispatch now: extract the row (layer 1), then draw it (layer 3). This function
 * used to answer four categories itself, ahead of the extraction -- the hidden row,
 * the stored control response, and LeapMux's own three assembled kinds -- and each
 * answer was a second spelling of one the extraction already held. The scroll rail
 * carried a third spelling of two of them.
 *
 * `parsedOrRawJson` is the provider payload, or the raw JSON text for a caller that
 * has not parsed it. It is read only when the caller supplies no resolved sources and
 * no extraction: every mounted row passes both.
 *
 * Returns an `UnrecognizedMessage` card when nothing could read the message, when the
 * JSON does not parse, and when a renderer throws -- the last-resort safety net.
 */
export function renderMessageContent(
  parsedOrRawJson: unknown,
  context?: RenderContext,
  category?: MessageCategory,
  agentProvider?: AgentProvider,
  messageCompletion?: MessageCompletion,
  extracted?: ChatRowExtraction,
): JSX.Element {
  try {
    // The caller's own extraction when it has one. `MessageBubble` supplies it, and
    // that matters beyond saving a lookup. This runs inside the body's render EFFECT,
    // so extracting here made the effect a reader of the to-do store: any create or
    // delete in the agent's list re-ran it and rebuilt the whole body DOM -- a fresh
    // markdown render, a fresh highlight dispatch, a tooltip lost under the pointer --
    // for a row whose content had not moved. Read through the bubble's memo, an
    // unchanged row is the same object and the effect does not re-run at all.
    if (extracted)
      return renderExtractedRow(extracted, context)

    // A control response takes NO payload, and the parse is skipped for it. The row
    // states LeapMux's own record of the answer, which the CATEGORY carries, and the
    // original content beside it can be a frame that never parsed -- an unparseable
    // original used to draw the unrecognized card instead of the answer, which is the
    // one thing the row exists to state.
    // The isolated-render fallback builds a parse no provider merged, so it
    // reaches the same resolved brand every mounted row reads.
    const parsed = context?.sources?.current()
      ?? resolveMessageForRendering(
        category?.kind === 'control_response'
          ? parsedMessageOf(undefined)
          : parsedMessageOf(typeof parsedOrRawJson === 'string' ? JSON.parse(parsedOrRawJson) : parsedOrRawJson),
        agentProvider ?? AgentProvider.UNSPECIFIED,
      )

    // The extraction is cached under the row's render-cache key, which already folds
    // the row's own content version and BOTH span siblings' revisions
    // (`renderKeyForEntry`). A stable object across renders is what keeps the row
    // from re-creating its DOM whenever an unrelated signal moves.
    //
    // Dispatch is strictly by the message's own provider -- no Claude fallback. An
    // unregistered or UNSPECIFIED provider yields no plugin, so the row reaches the
    // last-resort card rather than another provider's bytes drawn through Claude's
    // renderers. The rows LeapMux writes ITSELF answer before the plugin lookup, so a
    // tab whose worker metadata has not loaded still draws its own user sends.
    return renderExtractedRow(
      cachedChatRow(context, agentProvider, parsed, category ?? { kind: 'unknown' }, messageCompletion),
      context,
    )
  }
  catch (err) {
    // Only the JSON parse and the drawing itself remain in here: the extraction
    // catches its own throw and reports it as a `failed` outcome, which carries the
    // reason to the log at the extraction rather than to a second log line here.
    logger.warn('Failed to render message content:', err)
  }
  // The row reached no renderer, so it says so and keeps its frame in a collapsed body.
  // It used to print the frame itself as a paragraph of text, which is the raw-JSON row
  // the shared standard forbids -- a live census caught one on GitHub Copilot.
  //
  // `parsedOrRawJson` rather than the parse, because the throw above can BE the parse:
  // the raw text is then the only content this row has.
  const fallback = <UnrecognizedMessage payload={parsedOrRawJson} renderFailed {...(context !== undefined ? { context } : {})} />
  const marker = completionMarker(messageCompletionFromProto(messageCompletion))
  return marker
    ? (
        <>
          {fallback}
          <div role="note">{marker}</div>
        </>
      )
    : fallback
}
