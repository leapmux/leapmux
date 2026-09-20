import type { Component } from 'solid-js'
import type { ToolHeaderActionsCallerProps, ToolHeaderActionsLayoutProps } from './messageActions'
import type { MessageContextResolver } from './messageContextResolver'
import type { MessageRenderCache } from './messageRenderCache'
import type { MessageUiKey } from './messageUiKeys'
import type { ChatRow } from './model/row'
import type { RowRenderContext, ToolProgressSource } from './renderContext'
import type { ToolCallMeta } from './results/tools/meta'
import type { RowExtractionContext } from './rowModelCache'
import type { PreparedMessage } from './rowPreparation'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { BackgroundTaskItem } from '~/stores/chatBackgroundTasks'
import Check from 'lucide-solid/icons/check'
import Copy from 'lucide-solid/icons/copy'

import { createEffect, createMemo, createResource, ErrorBoundary, onCleanup, onMount, Show, untrack } from 'solid-js'
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
import { imageActionsFrom, subagentsFrom } from './renderContext'
import { quotableTextForRow } from './results/rowText'
import { toolCallMeta } from './results/tools/meta'
import { extractedRow } from './rowExtraction'
import { cachedChatRow } from './rowModelCache'
import { prepareMessage } from './rowPreparation'
import { renderExtractedRow } from './rowRenderers'
import { JsonHighlightHtml } from './syntaxHighlight'
import { ToolHeaderActions } from './ToolHeaderActions'

const logger = createLogger('MessageBubble')

function messageErrorFallback(label: string) {
  return (err: unknown) => {
    logger.warn(label, err)
    const message = formatErrorMessage(err)
    const rawStack = err instanceof Error ? err.stack : undefined
    const [resolved] = createResource(
      () => rawStack,
      stack => stack ? resolveStack(stack) : Promise.resolve(undefined),
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

function injectCopyButtons(container: HTMLElement): Array<() => void> {
  const disposers: Array<() => void> = []
  const preElements = container.querySelectorAll('pre')
  for (const pre of preElements) {
    if (pre.querySelector('.copy-code-button'))
      continue
    // Skip shiki <pre> inside tool messages — copy is handled by ToolHeaderActions.
    if (pre.closest('[data-tool-message]'))
      continue
    // The raw hidden-message JSON needs no guard here: it now renders as token <span>s
    // (JsonHighlightHtml), not a <pre>, so this querySelectorAll('pre') sweep never sees
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
          icon={copied() ? Check : Copy}
          title={copied() ? 'Copied' : 'Copy'}
          onClick={copy}
        />
      )
    }, host)

    disposers.push(dispose)
    // Mark the <pre> so the copy-button positioning (absolute, top-right) and its
    // relative anchor apply regardless of where the <pre> lives -- a markdown body or a
    // non-markdown block (e.g. a result-divider error <pre>). Without this the button
    // anchored only inside `.markdownContent` and fell inline elsewhere.
    pre.classList.add(codeCopyHostClass)
    pre.appendChild(host)
  }
  return disposers
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
  /** True while this row sits outside the near-viewport band. */
  rowOffscreen?: (() => boolean) | undefined
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
}

export const MessageBubble: Component<MessageBubbleProps> = (props) => {
  const prefs = usePreferences()
  const toolResultExpanded = () =>
    props.host?.getMessageUiState?.(MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED)
    ?? messageUiDefault(MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED)
  const toggleToolResultExpanded = () =>
    props.host?.setMessageUiState?.(MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED, !toolResultExpanded())
  let contentRef: HTMLDivElement | undefined

  // ChatView's own preparation when it supplied one, so the row the virtual list
  // MEASURED and the row this bubble DRAWS are read from one payload and one
  // category. A bubble mounted outside ChatView (a test, an isolated preview)
  // prepares the message itself.
  const resolved = createMemo(() => props.host?.messages?.resolvedMessage(props.message, props.prepared?.original))
  const prepared = createMemo<PreparedMessage>(() => {
    const resolvedParsed = resolved()?.resolved
    return props.prepared ?? prepareMessage(props.message, resolvedParsed !== undefined ? { resolved: resolvedParsed } : {})
  })
  const parsed = () => prepared().original
  const category = () => prepared().category
  const displayParsed = () => prepared().resolved
  const sources = createMessageRenderSources(() => props.host?.messages, () => prepared().message, displayParsed)

  // Full raw JSON for the Raw JSON display. Plain function (not createMemo)
  // so the JSON.parse + JSON.stringify only run when a consumer actually
  // reads it (Copy Raw JSON click, hidden-message <pre> render).
  // The raw-JSON debug envelope (hidden / unsupported_provider rows). The pure
  // builder lives in chatRawJson so its proto-field copying and parse-failure
  // fallbacks are unit-testable without mounting a component.
  const rawJson = (): string =>
    buildRawJsonEnvelope(resolved()?.message ?? props.message, resolved()?.original ?? parsed(), sourceLabel(props.message.source), props.host?.getHeightDebug?.())

  const { copied: jsonCopied, copy: copyJson } = useCopyButton(() => props.premeasureMode ? undefined : prettifyJson(rawJson()))

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

  createEffect(() => {
    const resolver = props.host?.messages
    const spanId = props.message.spanId
    const kind = category().kind
    if (!resolver || !spanId || props.premeasureMode || (kind !== 'tool_use' && kind !== 'tool_result'))
      return
    const release = resolver.retainRenderSpan({ spanId, agentSessionId: props.message.agentSessionId })
    onCleanup(release)
    void resolver.loadRelated(props.message, parsed()).catch((error: unknown) => {
      if (!(error instanceof DOMException && error.name === 'AbortError'))
        console.warn('Cannot load related tool messages', { spanId, error })
    })
  })

  // The three fields reading a row needs, built before the render context that
  // carries them: that context's `hasOuterToolbar` getter reads the toolbar, the
  // toolbar reads the row, and the row would otherwise read a context that does
  // not exist yet.
  const rowContext: RowExtractionContext = {
    sources,
    get renderCache() { return props.host?.renderCache },
    get spanType() { return props.message.spanType },
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
    return current?.kind === 'tool' ? toolCallMeta(current) : null
  })

  // The renderer renders its own ToolHeaderActions (inside ToolUseLayout) for
  // tool_use / agent_prompt — except when the row carries the RESULT of its
  // span, which puts its actions in the bubble's outer toolbar instead.
  const hasInternalActions = () => {
    const kind = category().kind
    if (kind !== 'tool_use' && kind !== 'agent_prompt')
      return false
    const current = row()
    return current?.kind !== 'tool' || current.role !== 'result'
  }

  const isCollapsibleToolResult = () => toolMeta()?.collapsible ?? false
  const hasToolResultDiff = () => toolMeta()?.hasDiff ?? false
  const hasCopyableResult = () => toolMeta()?.hasCopyable ?? false

  const { copied: resultCopied, copy: copyResultContent } = useCopyButton(() => props.premeasureMode ? undefined : toolMeta()?.copyableContent() ?? undefined)

  const diffView = () => props.host?.localDiffView ?? prefs.diffView()
  const toggleDiffView = () => props.host?.onSetLocalDiffView?.(diffView() === 'unified' ? 'split' : 'unified')

  // Memoize the wrapped onReply so callers reading `context.onReply` get a
  // stable reference between renders. Recomputes only when `props.onReply`
  // identity changes.
  const wrappedOnReply = createMemo(() => {
    const onReply = props.onReply
    return onReply ? (text: string) => onReply(formatChatQuote(text)) : undefined
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
      ...(image.filePath !== undefined ? { filePath: image.filePath } : {}),
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
    fileImage: (filePath, options) => sources.fileImage(filePath, options),
    cachedFileImage: filePath => sources.cachedFileImage(filePath),
    deferLoad: () => props.premeasureMode === true || props.host?.rowOffscreen?.() === true,
    premeasurePass: () => props.premeasureMode === true,
    ...(untrack(openImageHost) !== undefined ? { openImage } : {}),
  })
  const toolProgress: ToolProgressSource = { liveTail: () => sources.progress() }

  // Build render context for message renderers. A plain object literal with
  // getter accessors for reactive fields gives stable identity (allocated once
  // per component setup) AND per-field reactivity — body components track only
  // the getters they read, so changes to one field don't cascade to siblings.
  const renderContext: RowRenderContext = {
    get hasOuterToolbar() { return !hasInternalActions() },
    get workingDir() { return props.workingDir },
    get homeDir() { return props.homeDir },
    diffView,
    get onReply() { return wrappedOnReply() },
    ...(subagents !== undefined ? { subagents } : {}),
    ...(images !== undefined ? { images } : {}),
    toolProgress,
    get onCopyJson() { return copyJson },
    get jsonCopied() { return props.premeasureMode ? () => false : jsonCopied },
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
    get spanType() { return props.message.spanType },
    get getMessageUiState() { return props.host?.getMessageUiState },
    get setMessageUiState() { return props.premeasureMode ? undefined : props.host?.setMessageUiState },
    get premeasureMode() { return props.premeasureMode === true },
    get rowOffscreen() { return props.host?.rowOffscreen },
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

  const { copied: markdownCopied, copy: copyMarkdown } = useCopyButton(() => props.premeasureMode ? undefined : proseText() ?? undefined)

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
    let reinjectAfterSelection = false
    const isTextSelectionActive = () => untrack(() => props.host?.textSelectionActive?.() ?? false)
    const disposeAll = () => {
      for (const d of disposers)
        d()
      disposers = []
    }
    // (Re-)inject copy buttons over the CURRENT content. Disconnect the observer across
    // the injection so our own appendChild(host) writes don't re-trigger it (which would
    // loop). Dispose the prior roots first: an async re-render replaces the markdown
    // <div>'s innerHTML wholesale, orphaning the old buttons' Solid roots, so we drop them
    // and re-apply to the new <pre> elements.
    const reinject = () => {
      observer?.disconnect()
      disposeAll()
      disposers = injectCopyButtons(el)
      observer?.observe(el, { childList: true, subtree: true })
    }
    // Defer (re-)injection to idle: the querySelectorAll('pre') + per-<pre> button render
    // is post-mount chrome, not part of the first paint. A row that flings past unmounts
    // and cancels the handle before it fires, so the work is skipped for rows that scroll
    // by and runs only for rows that settle visible. The debounce also coalesces the burst
    // of mutations from one re-render into a single re-injection.
    const schedule = () => {
      if (isTextSelectionActive()) {
        reinjectAfterSelection = true
        return
      }
      if (idle !== undefined)
        cancelIdle(idle)
      idle = requestIdle(() => {
        idle = undefined
        if (isTextSelectionActive()) {
          reinjectAfterSelection = true
          return
        }
        reinject()
      })
    }
    createEffect(() => {
      if (props.host?.textSelectionActive?.())
        return
      if (!reinjectAfterSelection)
        return
      reinjectAfterSelection = false
      schedule()
    })
    // Re-apply on a CONTENT change, not just the first render: a code block's body is
    // produced by renderMarkdown, whose syntax highlighting now lands ASYNCHRONOUSLY (the
    // worker's highlighted HTML replaces the plain placeholder's innerHTML, wiping the
    // injected buttons). A one-shot injection raced that swap -- inject before it and the
    // button is wiped; after it and the button lands -- so a code block "sometimes" had no
    // copy button. Observing contentRef re-injects after the swap and after an
    // expand-collapse change, regardless of timing.
    //
    // But IGNORE mutations the copy buttons cause themselves -- the IconButton swapping its
    // Copy<->Check icon (and title) when clicked is a subtree mutation. Re-injecting on
    // that would dispose the button mid-click, wiping its transient "Copied" checkmark and
    // churning every button in the bubble on each copy. Re-inject only when a mutation
    // touches something OUTSIDE the copy-button chrome.
    const onMutations = (records: MutationRecord[]) => {
      for (const r of records) {
        const node = r.target
        const asEl = node instanceof Element ? node : node.parentElement
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
      ...(hasCopyableResult() ? { onCopyContent: copyResultContent } : {}),
      ...(copyContentLabel !== undefined ? { copyContentLabel } : {}),
      ...(quotableText() ? { onReply: props.premeasureMode ? () => {} : handleReply } : {}),
      ...(proseText() ? { onCopyMarkdown: copyMarkdown } : {}),
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
      ...(isCollapsibleToolResult() ? { onToggleExpand: toggleToolResultExpanded } : {}),
      ...(expandLabel !== undefined ? { expandLabel } : {}),
      ...(hasToolResultDiff() ? { onToggleDiffView: toggleDiffView } : {}),
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
          data-role={sourceLabel(props.message.source)}
        >
          <div ref={contentRef} data-testid="message-content">
            <ErrorBoundary fallback={messageErrorFallback('Failed to render message:')}>
              {category().kind === 'hidden'
                ? rawJsonBlock()
                : category().kind === 'unsupported_provider'
                  ? renderUnsupportedProvider()
                  : renderContent()}
            </ErrorBoundary>
          </div>
        </div>
        <Show when={!hasInternalActions()}>
          <ToolHeaderActions caller={actionsCaller()} layout={actionsLayout()} />
        </Show>
      </div>
    </div>
  )
}
