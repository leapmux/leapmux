import type { Component } from 'solid-js'
import type { PendingPreview } from './chatPendingRows'
import type { ToolHeaderActionsCallerProps, ToolHeaderActionsLayoutProps } from './messageActions'
import type { MessageContextResolver } from './messageContextResolver'
import type { MessageRenderCache } from './messageRenderCache'
import type { MessageUiKey } from './messageUiKeys'
import type { ChatRow } from './model/row'
import type { ToolCall } from './model/toolCall'
import type { RowRenderContext, ToolProgressSource } from './renderContext'
import type { RenderReadiness } from './renderReadiness'
import type { ToolCallMeta } from './results/tools/meta'
import type { RowExtractionContext } from './rowModelCache'
import type { PreparedMessage } from './rowPreparation'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { MessageSpanIdentity } from '~/lib/messageSpan'
import type { BackgroundTaskItem } from '~/stores/chatBackgroundTasks'
import Check from 'lucide-solid/icons/check'
import Copy from 'lucide-solid/icons/copy'

import { createEffect, createMemo, createResource, createSignal, ErrorBoundary, onCleanup, onMount, Show, untrack } from 'solid-js'
import { render } from 'solid-js/web'
import { agentProviderLabel } from '~/components/common/AgentProviderIcon'
import { attachContextMenuGesture } from '~/components/common/contextMenuGesture'
import { IconButton } from '~/components/common/IconButton'
import { usePreferences } from '~/context/PreferencesContext'
import { MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { useCopyButton } from '~/hooks/useCopyButton'
import { formatErrorMessage } from '~/lib/errors'
import { cancelIdle, requestIdle } from '~/lib/idleCallback'
import { prettifyJson } from '~/lib/jsonFormat'
import { createLogger } from '~/lib/logger'
import { messageSpanIdentity } from '~/lib/messageSpan'
import { formatChatQuote } from '~/lib/quoteUtils'
import { resolveStack } from '~/lib/resolveStack'
import { appendCompletionMarker } from './assembledMessage'
import { buildRawJsonEnvelope } from './chatRawJson'
import { codeCopyHostClass } from './markdownEditor/markdownContent.css'
import { buildMessageActions } from './messageActions'
import { useMessageContextMenu } from './MessageContextMenuHost'
import { createMessageRenderSources } from './messageContextResolver'
import { bubbleRunsToRightEdge, isMirroredMessageRow, messageBubbleClass, messageRowClass } from './messageRowLayout'
import * as chatStyles from './messageStyles.css'
import { expandedUiKeyFor, MESSAGE_UI_KEY, messageUiDefault } from './messageUiKeys'
import { rowDrawsResult } from './model/derivations'
import { imageActionsFrom, reviewActionsFrom, subagentsFrom } from './renderContext'
import { useRequestReadiness } from './renderReadiness'
import { quotableTextForRow } from './results/rowText'
import { pendingRowRequest, ToolPendingMessage } from './results/ToolPendingMessage'
import { rowExpandKey } from './results/toolRowState'
import { dispatchParts } from './results/tools/index'
import { toolCallMeta } from './results/tools/meta'
import { textNeedsCollapse } from './results/useCollapsedLines'
import { extractedRow } from './rowExtraction'
import { cachedChatRow, cachedSpanHeaderCall } from './rowModelCache'
import { prepareMessage } from './rowPreparation'
import { renderExtractedRow } from './rowRenderers'
import { JsonHighlightHtml } from './syntaxHighlight'
import { ToolHeaderActions } from './ToolHeaderActions'

const logger = createLogger('MessageBubble')

function messageErrorFallback(label: string) {
  return (err: unknown) => {
    logger.warn(label, err)
    const message = formatErrorMessage(err)
    const rawStack = err instanceof Error
      ? err.stack
      : undefined
    const [resolved] = createResource(
      () => rawStack,
      stack => stack
        ? resolveStack(stack)
        : Promise.resolve(undefined),
    )
    return (
      <span class={chatStyles.systemMessage}>
        {'Failed to render message: '}
        {message}
        <Show when={resolved() ?? rawStack}>
          {stack => <pre>{stack()}</pre>}
        </Show>
      </span>
    )
  }
}

function sourceLabel(source: MessageSource): string {
  switch (source) {
    case MessageSource.USER: return 'user'
    case MessageSource.AGENT: return 'agent'
    case MessageSource.LEAPMUX: return 'leapmux'
    // Only MESSAGE_SOURCE_UNSPECIFIED (proto 0) reaches here, and every
    // persistence path sets a real source -- an UNSPECIFIED row is a
    // misconfigured agent-side persistence bug. Surface it as 'unknown' (a
    // visibly anomalous data-role) instead of silently masquerading as 'agent',
    // matching the no-guessing stance for an unknown agentProvider.
    default: return 'unknown'
  }
}

/**
 * Give `pre` a copy button, and return the function that removes it again. Returns
 * undefined for a `<pre>` that needs no button.
 */
function injectCopyButton(pre: HTMLPreElement): (() => void) | undefined {
  if (pre.querySelector('.copy-code-button'))
    return undefined
  // Skip shiki <pre> inside tool messages — copy is handled by ToolHeaderActions.
  if (pre.closest('[data-tool-message]'))
    return undefined
  // The raw hidden-message JSON needs no guard here: it now renders as token <span>s
  // (JsonHighlightHtml), not a <pre>, so the querySelectorAll('pre') sweep never sees
  // it. Copy for that block is handled by the row's ToolHeaderActions.

  const host = document.createElement('div')
  host.style.display = 'contents'

  const dispose = render(() => {
    const { copied, copy } = useCopyButton(() => {
      const code = pre.querySelector('code')
      return code?.textContent || pre.textContent || ''
    })
    return (
      <IconButton
        class="copy-code-button"
        icon={copied()
          ? Check
          : Copy}
        title={copied()
          ? 'Copied'
          : 'Copy'}
        onClick={copy}
      />
    )
  }, host)

  // Mark the <pre> so the copy-button positioning (absolute, top-right) and its
  // relative anchor apply regardless of where the <pre> lives -- a markdown body or a
  // non-markdown block (e.g. a result-divider error <pre>). Without this the button
  // anchored only inside `.markdownContent` and fell inline elsewhere.
  pre.classList.add(codeCopyHostClass)
  pre.appendChild(host)
  // Disposing the root empties the host but leaves it in the <pre>, so remove it
  // too. A <pre> that outlives the injection would otherwise collect one empty host
  // for each injection that follows.
  return () => {
    dispose()
    host.remove()
  }
}

/**
 * Give a copy button to each `<pre>` that `pres` yields, until the idle period runs
 * out, and add each removal function to `disposers`. Handles one `<pre>` at least,
 * so the work advances even in an idle period that has no time left.
 *
 * Returns true when `pres` has no `<pre>` left, and false when it may still hold one.
 */
function injectCopyButtons(
  pres: Iterator<HTMLPreElement>,
  deadline: IdleDeadline,
  disposers: Array<() => void>,
): boolean {
  for (;;) {
    const next = pres.next()
    if (next.done === true)
      return true
    const dispose = injectCopyButton(next.value)
    if (dispose !== undefined)
      disposers.push(dispose)
    if (deadline.timeRemaining() <= 0)
      return false
  }
}

/**
 * ChatView-owned bindings exposed to a MessageBubble. Grouped here so the
 * bubble has a single host-side prop instead of a sprawling list of lifted
 * callbacks. Every field is optional — a bubble rendered outside ChatView
 * (tests, isolated previews) can pass `host={undefined}`. The members marked
 * `| undefined` are assigned by reactive getters that resolve through to
 * undefined while the host is absent/loading; a getter cannot omit a key, so
 * `undefined` is the live "absent for now" state rather than an invalid
 * construction.
 */
export interface MessageBubbleHost {
  /** Shared resolver for this agent's messages and live entities. */
  messages?: MessageContextResolver
  /** Open (or activate, or revive) a subagent's tab from its registry row. */
  onOpenSubagent?: ((item: BackgroundTaskItem) => void) | undefined
  /**
   * Open an image a row rendered in its own tab.
   *
   * `seq` is stamped by the bubble, which is the only layer that holds the
   * message; a renderer says only which image of its own row it means.
   */
  onOpenImage?: ((image: { seq: bigint, index: number, filePath?: string, title: string }) => void) | undefined
  /**
   * Record the reader's authorization for an action that automatic review denied in this row.
   * The control stays hidden while the agent cannot accept approval.
   */
  onApproveAutoReviewDenial?: ((messageId: string, reviewId: string) => Promise<void>) | undefined
  /** Lifted per-message diff view override, managed by ChatView. */
  localDiffView?: ('unified' | 'split') | undefined
  /** Set the per-message diff view override. */
  onSetLocalDiffView?: ((view: 'unified' | 'split') => void) | undefined
  /** Stable per-message UI state getter for remount-sensitive renderers. */
  getMessageUiState?: ((key: MessageUiKey) => boolean | undefined) | undefined
  /** Stable per-message UI state setter for remount-sensitive renderers. */
  setMessageUiState?: ((key: MessageUiKey, value: boolean) => void) | undefined
  /** Debug: this row's measured DOM height, for the raw-JSON surface. */
  getHeightDebug?: (() => { measured?: number }) | undefined
  /** Per-row/content-version cache for pure renderer derivations shared across hidden + visible mounts. */
  renderCache?: MessageRenderCache | undefined
  /** True while visible row rendering should avoid starting syntax-highlight jobs. */
  syntaxHighlightingPaused?: (() => boolean) | undefined
  /** True while the user has a live document selection inside the chat content. */
  textSelectionActive?: (() => boolean) | undefined
  /**
   * True while this row sits outside the near-viewport band. Not reactive to the scroll
   * position: the worker dispatch reads it at each dispatch.
   */
  rowOffscreen?: (() => boolean) | undefined
  /**
   * Whether this row is near the view, for the read of its file images. Reactive: the
   * host runs the test again when the answer can differ, when a scroll ends and when the
   * pane changes its height. A row whose host states none reads its images at once.
   */
  rowNearView?: (() => boolean) | undefined
  /**
   * Whether the pending row of this message's tool call is in the list (see
   * chatPendingRows): the row that draws what a running call asks. A host that lists no
   * pending row states none.
   */
  pendingRowOpen?: (() => boolean) | undefined
  pendingPreview?: (() => PendingPreview | undefined) | undefined
  /**
   * Whether the transcript draws the REQUEST of a span as its own row, also while that row
   * lies outside the loaded window (see createRequestRowPresence). A row of the span reads
   * it as `visibleRows.request`. A host that lists no rows states none, and the row then
   * draws its request side itself.
   */
  requestRowDrawn?: ((identity: MessageSpanIdentity) => boolean) | undefined
}

interface MessageBubbleProps {
  message: AgentChatMessage
  /**
   * The row as ChatView already prepared it -- the parse, the resolved payload and
   * the category, from the entry cache the virtual list measured the row from.
   *
   * Absent only outside ChatView, where the bubble prepares the message itself. It
   * used to arrive as the parse and the category SEPARATELY, and the bubble resolved
   * the payload after them -- so the row it drew was extracted from a payload its own
   * category had never seen.
   */
  prepared?: PreparedMessage
  workingDir?: string
  homeDir?: string
  onReply?: (quotedText: string) => void
  /** Lifted state and lookups owned by the parent ChatView. */
  host?: MessageBubbleHost
  /** Hidden premeasurement pass: keep layout structure, skip interactive/expensive chrome. */
  premeasureMode?: boolean
  /**
   * Draw the PENDING row of this message's tool call instead of the message's own row:
   * the preview of what the call asked and its live output, while the call runs (see
   * ToolPendingMessage). ChatView places such a row after the request row, and removes
   * it when the result row joins the list.
   */
  pending?: boolean
  /**
   * The readiness scope of this row copy. Each body that a worker renders -- a
   * Markdown body, or an edit diff (FileEditDiffBody) -- reports to it while its
   * render is pending, and a diff view reports to it until its last line mounts,
   * so the copy stays hidden and commits no height until every body is ready.
   * Absent outside ChatView.
   */
  readiness?: RenderReadiness
}

export const MessageBubble: Component<MessageBubbleProps> = (props) => {
  const prefs = usePreferences()
  let contentRef: HTMLDivElement | undefined

  // ChatView's own preparation when it supplied one, so the row the virtual list
  // MEASURED and the row this bubble DRAWS are read from one payload and one
  // category. A bubble mounted outside ChatView (a test, an isolated preview)
  // prepares the message itself.
  const resolved = createMemo(() => props.host?.messages?.resolvedMessage(props.message, props.prepared?.original))
  const prepared = createMemo<PreparedMessage>(() => {
    const resolvedParsed = resolved()?.resolved
    return props.prepared ?? prepareMessage(props.message, resolvedParsed !== undefined
      ? { resolved: resolvedParsed }
      : {})
  })
  const parsed = () => prepared().original
  const category = () => prepared().category
  const displayParsed = () => prepared().resolved
  const sources = createMessageRenderSources(
    () => props.host?.messages,
    () => prepared().message,
    displayParsed,
    identity => props.host?.requestRowDrawn?.(identity) ?? false,
  )

  // Full raw JSON for the Raw JSON display. Plain function (not createMemo)
  // so the JSON.parse + JSON.stringify only run when a consumer actually
  // reads it (Copy Raw JSON click, hidden-message <pre> render).
  // The raw-JSON debug envelope (hidden / unsupported_provider rows). The pure
  // builder lives in chatRawJson so its proto-field copying and parse-failure
  // fallbacks are unit-testable without mounting a component.
  const rawJson = (): string =>
    buildRawJsonEnvelope(resolved()?.message ?? props.message, resolved()?.original ?? parsed(), sourceLabel(props.message.source), props.host?.getHeightDebug?.())

  const { copied: jsonCopied, copy: copyJson } = useCopyButton(() => props.premeasureMode
    ? undefined
    : prettifyJson(rawJson()))

  // Reactive, memoized pretty raw JSON for the displayed block. Gated on the only two
  // categories that render it (hidden / unsupported_provider) so the proto-envelope
  // build + FracturedJson reformat stays lazy for every other bubble (matching rawJson's
  // plain-function intent), while the rows that DO show it reformat once per change
  // instead of on every TokenizedCode `props.code` read (2-3x per reactive pass).
  const prettyRawJson = createMemo(() => {
    const kind = category().kind
    if (kind !== 'hidden' && kind !== 'unsupported_provider')
      return ''
    return prettifyJson(rawJson())
  })

  // The fetch of the REQUEST of a tool call, as the bubble reports it to the readiness
  // scope of its row copy. A row asks for it when it depends on the request and the
  // browser does not hold it (see MessageContextResolver.loadRequest): a row that draws
  // from its request, and a result or update row, which draws its own header and request
  // side only while the transcript draws no request row beside it. A request body
  // never waits for its result. Its expanded header can wait for the later frame.
  //
  // - A hidden premeasure copy must not report a height before it shows what the row in
  //   the list will show: a height without the request is wrong, and the row would
  //   change it in the list when the request arrives.
  // - The row in the list must not show before it has the request either: it would
  //   draw again when the request arrives, and change its height in view. That is rare.
  //   The hidden copy of the row fetched the span before in the usual case. A row that a
  //   stored height admitted had no hidden copy in this session.
  //
  // The bubble reports only a fetch that the resolver really makes (see
  // MessageContextResolver.loadRequest). With no fetch, the row shows what it will
  // show, so it waits for nothing and shows at once.
  //
  // The report ends when the fetch settled, or at the limit of useRequestReadiness,
  // which aborts no fetch: the lookup goes on, and the row draws again when the request
  // arrives.
  const requestLookup = useRequestReadiness(() => props)
  const headerLookup = useRequestReadiness(() => props, untrack(() => props.premeasureMode)
    ? 'pending'
    : 'replacing')

  function reportLookup(fetch: Promise<void> | undefined, begin: () => () => void, spanId: string, warning: string): void {
    if (fetch === undefined)
      return
    const endWait = begin()
    onCleanup(endWait)
    void fetch
      .catch((error: unknown) => {
        if (!(error instanceof DOMException && error.name === 'AbortError'))
          console.warn(warning, { spanId, error })
      })
      .finally(endWait)
  }

  createEffect(() => {
    const resolver = props.host?.messages
    const spanId = props.message.spanId
    const kind = category().kind
    if (!resolver || !spanId || (kind !== 'tool_use' && kind !== 'tool_result'))
      return
    // The resolver counts its leases, so a hidden copy and the row can hold one span together.
    const release = resolver.retainRenderSpan({ spanId, agentSessionId: props.message.agentSessionId })
    onCleanup(release)
    reportLookup(resolver.loadRequest(props.message, parsed()), requestLookup.begin, spanId, 'Cannot load the request of a tool call')
  })

  // The three fields reading a row needs, built before the render context that
  // carries them: that context's `hasOuterToolbar` getter reads the toolbar, the
  // toolbar reads the row, and the row would otherwise read a context that does
  // not exist yet.
  const rowContext: RowExtractionContext = {
    sources,
    get renderCache() { return props.host?.renderCache },
    get spanType() { return props.message.spanType },
    get spanId() { return props.message.spanId },
  }

  // The row this bubble draws, read ONCE through the shared extraction and
  // cached under the row's own revision key. The toolbar below, the Quote and
  // Copy-Markdown actions, and the transcript row itself all read this one row,
  // so the toolbar can never describe a body the reader is not looking at.
  const extraction = createMemo(() => cachedChatRow(
    rowContext,
    props.message.agentProvider,
    displayParsed(),
    category(),
    props.message.completion,
  ))
  /** The drawn row, or null for a frame nobody could read (which draws the shared card). */
  const row = (): ChatRow | null => extractedRow(extraction())
  const toolRow = createMemo(() => {
    const current = row()
    return current?.kind === 'tool'
      ? current
      : null
  })

  // The expand state that the row's own Expand control reads (see rowExpandKey): the
  // request row of an agent launch reads the key of its prompt, every other row the key
  // of its result. The outer toolbar and the context menu toggle the same state.
  const toolExpandKey = createMemo((): MessageUiKey => {
    const current = toolRow()
    return current === null
      ? MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED
      : rowExpandKey(rowDrawsResult(current), dispatchParts(current.call))
  })
  const toolResultExpanded = () =>
    props.host?.getMessageUiState?.(toolExpandKey()) ?? messageUiDefault(toolExpandKey())
  const toggleToolResultExpanded = () =>
    props.host?.setMessageUiState?.(toolExpandKey(), !toolResultExpanded())

  // The fetch of the RESULT of the span, for the header of an expanded request row (see
  // MessageContextResolver.loadResult). Such a header wraps the words of the newest call of
  // its span (see toolHeader below), so the row copy waits for the lookup as it waits for
  // the request: a height measured with the words of the request alone changed when the
  // newer page that holds the result loaded.
  createEffect(() => {
    const resolver = props.host?.messages
    const current = toolRow()
    if (!resolver || props.pending || current === null || current.role === 'result' || !props.message.spanId || !toolResultExpanded())
      return
    const spanId = props.message.spanId
    reportLookup(resolver.loadResult(messageSpanIdentity(props.message)), headerLookup.begin, spanId, 'Cannot load the result of a tool call for its header')
  })

  // The newest call of this row's span that the browser holds, which a REQUEST row heads
  // itself with: the call the result row draws, read through the same extraction. Only
  // the one-line header reads it, and while the row is collapsed that header cannot
  // change the height of the row (see ToolMessage). The row's own body never reads it. The
  // pending row draws no header, so it reads none.
  //
  // The shared cache extracts the header once for each pair of revisions.
  const toolHeader = createMemo((): ToolCall | undefined => {
    const current = toolRow()
    if (props.pending || current === null || current.role === 'result')
      return undefined
    const result = sources.result()
    if (result === undefined || result.message.id === prepared().message.id)
      return undefined
    // The span's request: the row's own message unless it is a later row of the span,
    // such as an update row.
    return cachedSpanHeaderCall(result, sources.request() ?? displayParsed())
  })

  // Toolbar metadata for the current message — collapsibility, diff presence, the
  // two button labels, and a lazy copyable-content getter. Every tool row answers,
  // including a row that is still running: a long partial output is exactly what a
  // reader wants to expand and copy.
  //
  // The WHOLE `ToolCallMeta`, not the narrower `ToolResultMeta` it extends: the two
  // labels live on the wider type, and reading the memo through the narrow one made
  // them invisible here, so the outer toolbar dropped every word a kind states about
  // its own buttons. `previewText` rides along and belongs to the scroll rail
  // (~/components/chat/chatMarkPreview.ts); nothing in this file reads it.
  const toolMeta = createMemo<ToolCallMeta | null>(() => {
    const current = row()
    return current?.kind === 'tool'
      ? toolCallMeta(current)
      : null
  })

  // A tool request draws its actions inside ToolUseLayout.
  // A result uses the bubble's outer toolbar.
  const hasInternalActions = () => {
    const kind = category().kind
    if (kind !== 'tool_use')
      return false
    const current = row()
    return current?.kind !== 'tool' || current.role !== 'result'
  }

  // Whether the tool row offers Expand. The row reports it
  // (ToolResultRenderContext.onExpandableChange), because only the drawn row knows what
  // its header clips. A result row whose outcome note replaces its body draws no tool
  // row and reports nothing: nothing reads its expand state, so it offers no Expand.
  const [toolExpandable, setToolExpandable] = createSignal(false)
  // A row that draws no result offers the diff toggle of the changes that its call asks
  // for only while a row of the call draws them: the pending row below it, or its own
  // body for a call with no span (see ToolResultRenderContext.loneCall). Once the call
  // ends, no row of the call draws them, and the toggle would steer nothing.
  const hasToolResultDiff = (): boolean => {
    if (!(toolMeta()?.hasDiff ?? false))
      return false
    const current = toolRow()
    return current === null
      || rowDrawsResult(current)
      || props.message.spanId === ''
      || props.host?.pendingRowOpen?.() === true
  }
  const hasCopyableResult = () => toolMeta()?.hasCopyable ?? false

  const { copied: resultCopied, copy: copyResultContent } = useCopyButton(() => props.premeasureMode
    ? undefined
    : toolMeta()?.copyableContent() ?? undefined)

  const diffView = () => props.host?.localDiffView ?? prefs.diffView()
  const toggleDiffView = () => props.host?.onSetLocalDiffView?.(diffView() === 'unified'
    ? 'split'
    : 'unified')

  // Memoize the wrapped onReply so callers reading `context.onReply` get a
  // stable reference between renders. Recomputes only when `props.onReply`
  // identity changes.
  const wrappedOnReply = createMemo(() => {
    const onReply = props.onReply
    return onReply
      ? (text: string) => onReply(formatChatQuote(text))
      : undefined
  })

  // Stamp the message identity a renderer cannot know onto the open request.
  // Defined once so `renderContext.onOpenImage` keeps one identity across
  // reads -- a getter that built the closure per read would hand every reader a
  // different function and defeat the memos that capture it.
  //
  // The renderer's own `title` wins, because it is the layer that already has a
  // human name for the row -- an MCP body says "Playwright / screenshot" where
  // the span type says `mcp__playwright__screenshot`. Re-deriving that name
  // here would mean teaching a shared layer one provider's tool-name
  // convention, and it would be a second derivation of a name the renderer
  // computed a moment earlier. The span type is the fallback for a row whose
  // renderer has nothing better.
  //
  // Safe to take from the render pass because the title is STORED in the tab
  // payload at open time and read back from it -- unlike `index`, nothing ever
  // re-derives it, so there is no second side for it to disagree with.
  const openImageHost = createMemo(() => props.host?.onOpenImage)
  const openImage = (image: { index: number, filePath?: string, title?: string }) => {
    openImageHost()?.({
      seq: props.message.seq,
      index: image.index,
      ...(image.filePath !== undefined
        ? { filePath: image.filePath }
        : {}),
      title: image.title || props.message.spanType || 'Image',
    })
  }

  // The narrow capabilities the shared result components accept. Built ONCE
  // beside the context that carries them, for the same identity reason as
  // `openImage` above: a getter that re-assembled per read would hand every
  // reader a different object and defeat the memos that capture it. The members
  // stay lazy, so each read tracks only what it asks for.
  const subagents = subagentsFrom({
    backgroundTask: key => sources.backgroundTask(key),
    openSubagent: item => props.host?.onOpenSubagent?.(item),
  })
  const images = imageActionsFrom({
    files: sources.fileImages,
    // The read of an image whose size is known waits in a hidden copy, and in a row of the
    // list that is far from the view (see ImageRenderActions.deferLoad). The host states
    // whether the row is near the view, and runs its test again when the answer can differ
    // (see MessageBubbleHost.rowNearView).
    deferLoad: () => props.premeasureMode === true || props.host?.rowNearView?.() === false,
    premeasurePass: () => props.premeasureMode === true,
    ...(untrack(openImageHost) !== undefined
      ? { openImage }
      : {}),
  })
  const toolProgress: ToolProgressSource = {
    liveTail: () => sources.progress(),
  }
  const reviews = reviewActionsFrom({
    messageId: () => props.message.id,
    approveAutoReviewDenial: () => props.host?.onApproveAutoReviewDenial,
  })

  // Build render context for message renderers. A plain object literal with
  // getter accessors for reactive fields gives stable identity (allocated once
  // per component setup) AND per-field reactivity — body components track only
  // the getters they read, so changes to one field don't cascade to siblings.
  const renderContext: RowRenderContext = {
    get hasOuterToolbar() { return !hasInternalActions() },
    onExpandableChange: expandable => setToolExpandable(expandable),
    get loneCall() { return props.message.spanId === '' },
    get workingDir() { return props.workingDir },
    get homeDir() { return props.homeDir },
    diffView,
    get onReply() { return wrappedOnReply() },
    ...(subagents !== undefined
      ? { subagents }
      : {}),
    images,
    // A getter, so the control follows the host: the agent gains and loses the
    // capability as it starts and stops.
    get reviews() {
      return props.host?.onApproveAutoReviewDenial !== undefined
        ? reviews
        : undefined
    },
    toolProgress,
    get headerExpansionPending() { return headerLookup.pending() },
    get hasPendingPreview() { return props.host?.pendingRowOpen?.() === true },
    get pendingPreviewExpandable() {
      return !props.pending && props.host?.pendingRowOpen?.() === true
        && textNeedsCollapse(props.host?.pendingPreview?.()?.outputTail ?? '')
    },
    get onCopyJson() { return copyJson },
    get jsonCopied() {
      return props.premeasureMode
        ? () => false
        : jsonCopied
    },
    get createdAt() { return props.message.createdAt },
    get expandAgentThoughts() { return prefs.expandAgentThoughts() },
    // Resolve the row's expand-toggle UI key ONCE here (kind + provider), so the
    // thinking-style renderers read the same key ChatView resolved -- see
    // expandedUiKeyFor. Getter so the literal stays referentially stable while
    // tracking a category/provider change.
    get expandUiKey() { return expandedUiKeyFor(category().kind) },
    get renderCache() { return props.host?.renderCache },
    syntaxHighlightingPaused: () => props.host?.syntaxHighlightingPaused?.() ?? false,
    textSelectionActive: () => props.host?.textSelectionActive?.() ?? false,
    get spanColor() { return props.message.spanColor },
    get getMessageUiState() { return props.host?.getMessageUiState },
    get setMessageUiState() { return props.premeasureMode ? undefined : props.host?.setMessageUiState },
    get premeasureMode() { return props.premeasureMode === true },
    get rowOffscreen() { return props.host?.rowOffscreen },
    get readiness() { return props.readiness },
    get toolHeader() { return toolHeader() },
  }

  // The row's own PROSE: what the agent said, what it thought, the plan it proposed,
  // what the reader typed. Read from the ROW, whatever produced it -- the worker's
  // assembled envelope reaches the same three prose rows through layer 1, so this no
  // longer parses that envelope a second time. This is what Copy-Markdown writes.
  const proseText = createMemo(() => {
    const text = quotableTextForRow(row())
    if (text === null)
      return null
    return appendCompletionMarker(text, extraction().completion)
  })

  /**
   * The text Quote writes, for EVERY row that carries one.
   *
   * A prose row answers from the row itself. A tool row answers from its kind's
   * meta -- the same getter its Copy button writes -- so Copy and Quote can never
   * state two different texts for one row, whatever the kind. This is the one place
   * the rule lives, and both the hover toolbar and the row's context menu read it.
   *
   * Not `previewText()`, which is the scroll rail's snippet and is deliberately
   * unlike a quote: a file-change row answers it with the file PATHS, and a task row
   * with `title - output`. Quoting a list of paths into the composer is a bug.
   *
   * No completion marker on the tool branch, because Copy states none either.
   *
   * No CAP on the length either, and that is deliberate. A whole unified diff or a
   * long grep result reaches the composer in one click. Copy already writes that same
   * text with no cap, so a cap here alone would break the one property this rule
   * exists to hold -- and the quote lands in a draft the reader edits or deletes, well
   * under the message ceiling the channel negotiates. A cap must also TELL the reader
   * where it cut, which puts a marker in their draft for them to remove.
   */
  const quotableText = createMemo(() => proseText() ?? toolMeta()?.copyableContent() ?? null)

  const handleReply = () => {
    const text = quotableText()
    if (text && props.onReply) {
      props.onReply(formatChatQuote(text))
    }
  }

  const { copied: markdownCopied, copy: copyMarkdown } = useCopyButton(() => props.premeasureMode
    ? undefined
    : proseText() ?? undefined)

  const rowClass = () => messageRowClass(category().kind, props.message.source)
  const bubbleClass = () => {
    const base = messageBubbleClass(category().kind, props.message.source)
    return bubbleRunsToRightEdge(category().kind, props.message.source)
      ? `${base} ${chatStyles.bubbleFlushRight}`
      : base
  }

  // Render the message body from the row that this bubble already extracted. The
  // renderer receives row capabilities and message metadata, not resolver sources.
  // A frame that no extractor claims still reaches the shared raw-payload card.
  const renderContent = () =>
    renderExtractedRow(extraction(), renderContext, displayParsed().messageMetadata)

  // Only a request has a pending preview. Its output belongs to the pending
  // row owner so a remounted copy can draw the same text.
  const renderPending = () => {
    const current = pendingRowRequest(toolRow())
    return current !== null
      ? <ToolPendingMessage row={current} context={renderContext} preview={props.host?.pendingPreview} />
      : null
  }

  // The raw-JSON last-resort block (highlighted as token spans via the async
  // token worker), shared by the `hidden` category and the unsupported-provider
  // error surface so the rendering lives in one place and can't drift. Copy is
  // handled by the row's ToolHeaderActions; rendering token <span>s (not a
  // shiki <pre>) means the markdown copy-button injector never targets it.
  const rawJsonBlock = () => (
    <JsonHighlightHtml class={chatStyles.hiddenMessageJson} code={prettyRawJson()} context={renderContext} />
  )

  // Loud surface for a message whose `agentProvider` is UNSPECIFIED or has no
  // registered plugin (classify returns `unsupported_provider`). We refuse to
  // guess another provider's renderer, so show an explicit error plus the raw
  // JSON for debugging -- a visible misconfiguration, not a silent mis-render.
  const renderUnsupportedProvider = () => (
    <>
      <div style={{ color: 'var(--danger)' }}>
        {`Unsupported agent provider: ${agentProviderLabel(props.message.agentProvider)} (${props.message.agentProvider}) -- cannot render this message.`}
      </div>
      {rawJsonBlock()}
    </>
  )

  onMount(() => {
    if (props.premeasureMode)
      return
    if (!contentRef)
      return
    const el = contentRef
    let disposers: Array<() => void> = []
    let idle: number | undefined
    let observer: MutationObserver | undefined
    // The <pre> elements of the current content that the injection has not reached
    // yet. Undefined when the next slice must start over the current content.
    let pendingPres: Iterator<HTMLPreElement> | undefined
    let resumeAfterSelection = false
    const isTextSelectionActive = () => untrack(() => props.host?.textSelectionActive?.() ?? false)
    const disposeAll = () => {
      for (const d of disposers)
        d()
      disposers = []
    }
    // An injection under a live selection would move the selected nodes. While a
    // selection is active, return true, and resume the injection when it ends.
    const waitsForSelection = (): boolean => {
      if (!isTextSelectionActive())
        return false
      resumeAfterSelection = true
      return true
    }
    // Inject copy buttons into the CURRENT content for the rest of one idle period.
    // Returns true when the content has no <pre> left. Disconnect the observer across
    // the slice so our own appendChild(host) writes don't re-trigger it (which would
    // loop). A new run disposes the prior roots first: an async re-render replaces the
    // markdown <div>'s innerHTML wholesale, orphaning the old buttons' Solid roots, so
    // we drop them and re-apply to the new <pre> elements.
    const injectSlice = (deadline: IdleDeadline): boolean => {
      observer?.disconnect()
      if (pendingPres === undefined) {
        disposeAll()
        pendingPres = el.querySelectorAll('pre').values()
      }
      const done = injectCopyButtons(pendingPres, deadline, disposers)
      observer?.observe(el, { childList: true, subtree: true })
      if (done)
        pendingPres = undefined
      return done
    }
    // Defer injection to idle: the querySelectorAll('pre') + per-<pre> button render
    // is post-mount chrome, not part of the first paint. A row that flings past unmounts
    // and cancels the handle before it fires, so the work is skipped for rows that scroll
    // by and runs only for rows that settle visible. The injection continues in the next
    // idle period while <pre> elements remain: a body can hold thousands of code blocks,
    // and one Solid root for each in a single task would hold the main thread for
    // seconds.
    const requestSlice = (): void => {
      if (idle !== undefined)
        cancelIdle(idle)
      idle = undefined
      if (waitsForSelection())
        return
      idle = requestIdle((deadline) => {
        idle = undefined
        if (waitsForSelection())
          return
        if (!injectSlice(deadline))
          requestSlice()
      })
    }
    // Start over the current content. The debounce also coalesces the burst of
    // mutations from one re-render into a single new run.
    const schedule = () => {
      pendingPres = undefined
      requestSlice()
    }
    createEffect(() => {
      if (props.host?.textSelectionActive?.())
        return
      if (!resumeAfterSelection)
        return
      resumeAfterSelection = false
      requestSlice()
    })
    // Re-apply on a CONTENT change, not just the first render: a Markdown body's HTML
    // lands ASYNCHRONOUSLY from the worker (it replaces the body's pending skeleton, and a
    // theme change replaces the HTML again, wiping the injected buttons). A one-shot
    // injection raced that swap -- inject before it and the button is wiped; after it and
    // the button lands -- so a code block "sometimes" had no copy button. Observing
    // contentRef re-injects after the swap and after an expand-collapse change,
    // regardless of timing.
    //
    // But IGNORE mutations the copy buttons cause themselves -- the IconButton swapping its
    // Copy<->Check icon (and title) when clicked is a subtree mutation. Re-injecting on
    // that would dispose the button mid-click, wiping its transient "Copied" checkmark and
    // churning every button in the bubble on each copy. Re-inject only when a mutation
    // touches something OUTSIDE the copy-button chrome.
    const onMutations = (records: MutationRecord[]) => {
      for (const r of records) {
        const node = r.target
        const asEl = node instanceof Element
          ? node
          : node.parentElement
        if (!asEl?.closest('.copy-code-button')) {
          schedule()
          return
        }
      }
    }
    observer = new MutationObserver(onMutations)
    observer.observe(el, { childList: true, subtree: true })
    schedule()
    onCleanup(() => {
      observer?.disconnect()
      if (idle !== undefined)
        cancelIdle(idle)
      disposeAll()
    })
  })

  // The two action bags, read by the hover toolbar AND by the row's context menu
  // (below). One definition, so the menu can never offer a different set from the
  // toolbar -- see `buildMessageActions` in ~/components/chat/messageActions.ts.
  //
  // Copy-Markdown stays on the PROSE text alone. A tool row already carries Copy,
  // worded by its own kind, over exactly the text Quote would write -- so keying
  // Copy-Markdown to `quotableText` would put two buttons on that row that copy the
  // same string under two names.
  const actionsCaller = createMemo((): ToolHeaderActionsCallerProps => {
    const copyContentLabel = toolMeta()?.copyLabel
    return {
      contentCopied: resultCopied(),
      markdownCopied: markdownCopied(),
      ...(hasCopyableResult()
        ? { onCopyContent: copyResultContent }
        : {}),
      ...(copyContentLabel !== undefined
        ? { copyContentLabel }
        : {}),
      ...(quotableText()
        ? {
            onReply: props.premeasureMode
              ? () => {}
              : handleReply,
          }
        : {}),
      ...(proseText()
        ? { onCopyMarkdown: copyMarkdown }
        : {}),
    }
  })

  const actionsLayout = createMemo((): ToolHeaderActionsLayoutProps => {
    const expandLabel = toolMeta()?.expandLabel
    return {
      // A right-aligned user row mirrors its toolbar beside the bubble, so it
      // reverses the button order -- read from the same predicate that picks the row
      // class, never re-derived here.
      mirrored: isMirroredMessageRow(category().kind, props.message.source),
      createdAt: props.message.createdAt,
      expanded: toolResultExpanded(),
      onCopyJson: copyJson,
      jsonCopied: jsonCopied(),
      hasDiff: hasToolResultDiff(),
      diffView: diffView(),
      ...(toolExpandable()
        ? { onToggleExpand: toggleToolResultExpanded }
        : {}),
      ...(expandLabel !== undefined
        ? { expandLabel }
        : {}),
      ...(hasToolResultDiff()
        ? { onToggleDiffView: toggleDiffView }
        : {}),
    }
  })
  // Right-click / long-press anywhere on the row opens the same actions the hover
  // toolbar carries. Attached here rather than through `DropdownMenu`'s
  // `contextMenuFor`, because the menu itself is a singleton for the whole list --
  // see ~/components/chat/MessageContextMenuHost.tsx for why a per-row menu is not
  // affordable in a virtualized list.
  const contextMenu = useMessageContextMenu()

  function attachRowMenu(el: HTMLElement) {
    // The hidden premeasure pass renders a second copy of every unmeasured row. It
    // is `visibility: hidden` and `pointer-events: none`, so it can never receive
    // the gesture -- but arming one per copy would still churn listeners on every
    // fling for nothing.
    // The pending row shares the request's copy and diff actions. Expand belongs to
    // the request row, which holds the expansion control.
    if (!contextMenu || props.premeasureMode)
      return
    const detach = attachContextMenuGesture(el, {
      // Message bodies are prose. Keep them selectable, and let a right-click on a
      // live selection fall through to the browser's own Copy.
      selectableText: true,
      onOpen: press => contextMenu.open({
        press,
        // The menu is the ONE surface that also carries the recovery actions: the
        // failed row renders them as visible buttons, but a user who reached for
        // the menu should not have to go looking for them somewhere else.
        actions: buildMessageActions(actionsCaller(), actionsLayout()),
        createdAt: props.message.createdAt,
      }),
      onCancel: () => contextMenu.close(),
    })
    onCleanup(detach)
  }

  return (
    <div style={{ display: 'contents' }}>
      {/* eslint-disable-next-line solid/reactivity -- a ref callback, not a signal to read */}
      <div class={rowClass()} ref={attachRowMenu}>
        <div
          class={bubbleClass()}
          data-testid="message-bubble"
          data-category={category().kind}
          data-role={sourceLabel(props.message.source)}
          data-tool-call-id={toolRow()?.call.id}
          data-tool-row-role={props.pending
            ? 'pending'
            : toolRow()?.role}
          data-tool-status={toolRow()?.call.status}
          data-message-seq={toolRow()
            ? String(props.message.seq)
            : undefined}
        >
          <div ref={contentRef} data-testid="message-content">
            <ErrorBoundary fallback={messageErrorFallback('Failed to render message:')}>
              {props.pending
                ? renderPending()
                : category().kind === 'hidden'
                  ? rawJsonBlock()
                  : category().kind === 'unsupported_provider'
                    ? renderUnsupportedProvider()
                    : renderContent()}
            </ErrorBoundary>
          </div>
        </div>
        <Show when={!props.pending && !hasInternalActions()}>
          <ToolHeaderActions caller={actionsCaller()} layout={actionsLayout()} />
        </Show>
      </div>
    </div>
  )
}
