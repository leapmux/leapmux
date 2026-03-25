import type { Component } from 'solid-js'
import type { MessageCategory } from './messageClassification'
import type { RenderContext } from './messageRenderers'
import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { CommandStreamSegment } from '~/stores/chat.store'

import Check from 'lucide-solid/icons/check'
import Copy from 'lucide-solid/icons/copy'
import { createMemo, createSignal, ErrorBoundary, onMount, Show } from 'solid-js'
import { render } from 'solid-js/web'
import { IconButton } from '~/components/common/IconButton'
import { usePreferences } from '~/context/PreferencesContext'
import { AgentProvider, MessageRole } from '~/generated/leapmux/v1/agent_pb'
import { createLogger } from '~/lib/logger'
import { parseMessageContent } from '~/lib/messageParser'
import { formatChatQuote } from '~/lib/quoteUtils'
import { formatUnifiedDiffText, rawDiffToHunks } from './diffUtils'
import * as styles from './MessageBubble.css'
import { classifyMessage, messageBubbleClass, messageRowClass } from './messageClassification'
import { renderMessageContent, ToolHeaderActions } from './messageRenderers'
import * as chatStyles from './messageStyles.css'
import { getAssistantContent, isObject } from './messageUtils'
import { renderNotificationThread } from './notificationRenderers'
import { prettifyJson } from './rendererUtils'
import { COLLAPSED_RESULT_ROWS } from './toolRenderers'

const logger = createLogger('MessageBubble')

function renderErrorFallback(label: string) {
  return (err: unknown) => {
    logger.warn(label, err)
    const message = err instanceof Error ? err.message : String(err)
    const stack = err instanceof Error ? err.stack : undefined
    return (
      <span class={chatStyles.systemMessage}>
        {'Failed to render message: '}
        {message}
        <Show when={stack}>
          <pre>{stack}</pre>
        </Show>
      </span>
    )
  }
}

function roleLabel(role: MessageRole): string {
  switch (role) {
    case MessageRole.USER: return 'user'
    case MessageRole.ASSISTANT: return 'assistant'
    case MessageRole.SYSTEM: return 'system'
    case MessageRole.RESULT: return 'result'
    case MessageRole.LEAPMUX: return 'leapmux'
    default: return 'system'
  }
}

function injectCopyButtons(container: HTMLElement) {
  const preElements = container.querySelectorAll('pre')
  for (const pre of preElements) {
    if (pre.querySelector('.copy-code-button'))
      continue
    // Skip shiki <pre> inside tool messages — copy is handled by ToolHeaderActions.
    if (pre.closest('[data-tool-message]'))
      continue

    const host = document.createElement('div')
    host.style.display = 'contents'

    render(() => {
      const [copied, setCopied] = createSignal(false)

      return (
        <IconButton
          class="copy-code-button"
          icon={copied() ? Check : Copy}
          title={copied() ? 'Copied' : 'Copy'}
          onClick={() => {
            const code = pre.querySelector('code')
            const text = code?.textContent || pre.textContent || ''
            navigator.clipboard.writeText(text).then(() => {
              setCopied(true)
              setTimeout(setCopied, 2000, false)
            })
          }}
        />
      )
    }, host)

    pre.appendChild(host)
  }
}

function isCodexEmptyCompletedWebSearch(message: AgentChatMessage, parsed: ParsedMessageContent): boolean {
  if (message.agentProvider !== AgentProvider.CODEX || message.spanType !== 'webSearch')
    return false

  const item = parsed.parentObject?.item as Record<string, unknown> | undefined
  if (!isObject(item) || item.type !== 'webSearch')
    return false

  const query = typeof item.query === 'string' ? item.query.trim() : ''
  const action = isObject(item.action) ? item.action as Record<string, unknown> : null
  const actionType = typeof action?.type === 'string' ? action.type as string : ''

  if (actionType !== 'other')
    return false

  return query.length === 0
}

/** Classify a message, returning both the parsed content and category. */
export function classifyParsedMessage(message: AgentChatMessage) {
  const parsed = parseMessageContent(message)
  let category = classifyMessage(parsed.parentObject, parsed.wrapper, message.agentProvider)
  // Hide task_started/task_progress system messages inside spans — progress is shown via the span's context.
  if (category.kind === 'notification' && message.parentSpanId
    && (parsed.parentObject?.subtype === 'task_started' || parsed.parentObject?.subtype === 'task_progress')) {
    category = { kind: 'hidden' }
  }
  // Hide ToolSearch tool_use and tool_result — they are internal plumbing, not user-visible.
  if ((category.kind === 'tool_use' || category.kind === 'tool_result') && message.spanType === 'ToolSearch') {
    category = { kind: 'hidden' }
  }
  // Hide TodoWrite tool_result — the tool_use already shows the full todo list.
  if (category.kind === 'tool_result' && message.spanType === 'TodoWrite') {
    category = { kind: 'hidden' }
  }
  if ((category.kind === 'tool_use' || category.kind === 'tool_result') && isCodexEmptyCompletedWebSearch(message, parsed)) {
    category = { kind: 'hidden' }
  }
  return { parsed, category }
}

interface MessageBubbleProps {
  message: AgentChatMessage
  parsed?: ParsedMessageContent
  category?: MessageCategory
  error?: string
  onRetry?: () => void
  onDelete?: () => void
  workingDir?: string
  homeDir?: string
  onReply?: (quotedText: string) => void
  /** Look up a message by its spanId (for tool_use ↔ tool_result linking). */
  getMessageBySpanId?: (spanId: string) => AgentChatMessage | undefined
  commandStream?: CommandStreamSegment[]
}

export const MessageBubble: Component<MessageBubbleProps> = (props) => {
  const prefs = usePreferences()
  const [jsonCopied, setJsonCopied] = createSignal(false)
  const [markdownCopied, setMarkdownCopied] = createSignal(false)
  const [toolResultExpanded, setToolResultExpanded] = createSignal(false)
  const [localDiffView, setLocalDiffView] = createSignal<'unified' | 'split' | null>(null)
  let contentRef: HTMLDivElement | undefined

  // Use pre-computed values from ChatView when available, otherwise compute on demand.
  const classified = createMemo(() => props.parsed && props.category
    ? { parsed: props.parsed, category: props.category }
    : classifyParsedMessage(props.message))
  const parsed = () => classified().parsed
  const category = () => classified().category

  // Full raw JSON for the Raw JSON display (only stringified on demand for "Copy Raw JSON").
  const rawJson = (): string => {
    const p = parsed()
    const msg = props.message
    const envelope: Record<string, unknown> = {
      id: msg.id,
      role: roleLabel(msg.role),
      seq: Number(msg.seq),
      created_at: msg.createdAt,
    }
    if (msg.deliveryError)
      envelope.delivery_error = msg.deliveryError
    if (msg.depth)
      envelope.depth = msg.depth
    if (msg.spanId)
      envelope.span_id = msg.spanId
    if (msg.parentSpanId)
      envelope.parent_span_id = msg.parentSpanId
    if (msg.spanType)
      envelope.span_type = msg.spanType
    if (msg.spanColor > 0)
      envelope.span_color = msg.spanColor
    if (msg.spanLines && msg.spanLines !== '[]')
      envelope.span_lines = JSON.parse(msg.spanLines)
    if (p.wrapper && p.wrapper.old_seqs.length > 0)
      envelope.old_seqs = p.wrapper.old_seqs

    if (p.wrapper) {
      envelope.messages = p.wrapper.messages
      return JSON.stringify(envelope)
    }

    try {
      envelope.content = JSON.parse(p.rawText)
      return JSON.stringify(envelope)
    }
    catch {
      return p.rawText
    }
  }

  const copyJson = async () => {
    await navigator.clipboard.writeText(prettifyJson(rawJson()))
    setJsonCopied(true)
    setTimeout(setJsonCopied, 2000, false)
  }

  // Look up the corresponding tool_use message for tool_result messages.
  const toolUseMessage = createMemo(() => {
    if (category().kind !== 'tool_result')
      return undefined
    const spanId = props.message.spanId
    if (!spanId || !props.getMessageBySpanId)
      return undefined
    return props.getMessageBySpanId(spanId)
  })

  /** Extract the raw text content from a tool_result block inside a parsed message object. */
  function extractToolResultText(obj: Record<string, unknown>): string | null {
    const msg = obj.message as Record<string, unknown> | undefined
    if (!msg || !Array.isArray(msg.content))
      return null
    const tr = (msg.content as Array<Record<string, unknown>>).find(c => isObject(c) && c.type === 'tool_result')
    if (!tr)
      return null
    const text = Array.isArray(tr.content)
      ? (tr.content as Array<Record<string, unknown>>).filter(c => isObject(c) && c.type === 'text').map(c => c.text).join('')
      : String(tr.content || '')
    return text || null
  }

  const isCodexTerminalCommandResult = createMemo(() => {
    if (props.message.agentProvider !== AgentProvider.CODEX)
      return false
    if (category().kind !== 'tool_use' || props.message.spanType !== 'commandExecution')
      return false
    const item = parsed().parentObject?.item as Record<string, unknown> | undefined
    const status = typeof item?.status === 'string' ? item.status : ''
    return status === 'completed' || status === 'failed'
  })

  const isCodexCompletedFileChangeResult = createMemo(() => {
    if (props.message.agentProvider !== AgentProvider.CODEX)
      return false
    if (category().kind !== 'tool_use' || props.message.spanType !== 'fileChange')
      return false
    const item = parsed().parentObject?.item as Record<string, unknown> | undefined
    return item?.status === 'completed'
  })

  // Whether the message is rendered by a renderer that has its own internal ToolHeaderActions.
  // tool_use and agent_prompt render their own ToolHeaderActions inside ToolUseLayout,
  // except Codex terminal command results, which should use the shared result toolbar.
  const hasInternalActions = () => {
    if (isCodexTerminalCommandResult() || isCodexCompletedFileChangeResult())
      return false
    return category().kind === 'tool_use' || category().kind === 'agent_prompt'
  }

  // Shared derivation for tool_result messages: extract toolName and toolUseResult once.
  const toolResultInfo = createMemo(() => {
    if (category().kind !== 'tool_result')
      return null
    const obj = parsed().parentObject
    if (!obj)
      return null
    const toolUseResult = obj.tool_use_result as Record<string, unknown> | undefined
    const toolName = props.message.spanType || String(toolUseResult?.tool_name || '')
    return { obj, toolUseResult, toolName }
  })

  // Determine if this tool_result is collapsible (enough lines/items to warrant collapse).
  const isCollapsibleToolResult = createMemo(() => {
    if (isCodexTerminalCommandResult()) {
      const item = parsed().parentObject?.item as Record<string, unknown> | undefined
      const output = typeof item?.aggregatedOutput === 'string' ? item.aggregatedOutput : ''
      return output.split('\n').length > COLLAPSED_RESULT_ROWS
    }

    const info = toolResultInfo()
    if (!info)
      return false

    const { obj, toolUseResult, toolName } = info

    // Grep/Glob: collapsible based on structured data or raw result content.
    if (toolName === 'Grep' || toolName === 'Glob') {
      // Structured: check filenames array from tool_use_result.
      const filenames = Array.isArray(toolUseResult?.filenames) ? toolUseResult!.filenames as string[] : []
      if (filenames.length > COLLAPSED_RESULT_ROWS)
        return true
      // Structured: check content lines from tool_use_result (Grep content mode).
      if (toolName === 'Grep' && typeof toolUseResult?.content === 'string'
        && toolUseResult.content.split('\n').length > COLLAPSED_RESULT_ROWS) {
        return true
      }
      // Fallback: check raw result content (e.g. subagent without tool_use_result).
      const rc = extractToolResultText(obj)
      return rc != null && rc.split('\n').filter((l: string) => l.trim()).length > COLLAPSED_RESULT_ROWS
    }

    // Read with structured file data: use numLines directly.
    if (toolName === 'Read') {
      const file = toolUseResult?.file as Record<string, unknown> | undefined
      if (file && typeof file.numLines === 'number')
        return file.numLines > COLLAPSED_RESULT_ROWS
    }

    // Agent: collapsible when structured content is present, or when raw result text is long enough (async launches).
    if (toolName === 'Agent') {
      if (Array.isArray(toolUseResult?.content)
        && (toolUseResult!.content as Array<Record<string, unknown>>).some(c => typeof c === 'object' && c !== null && c.type === 'text')) {
        return true
      }
      const rc = extractToolResultText(obj)
      return rc != null && rc.split('\n').length > COLLAPSED_RESULT_ROWS
    }

    // WebFetch: always collapsible when structured result is present.
    if (toolName === 'WebFetch' && typeof toolUseResult?.code === 'number')
      return true

    // WebSearch: always collapsible when structured results are present.
    if (toolName === 'WebSearch' && Array.isArray(toolUseResult?.results))
      return true

    // Bash/Read/TaskOutput/unknown: collapsible if result text exceeds threshold lines.
    if (toolName === 'Bash' || toolName === 'Read' || toolName === 'TaskOutput' || toolName === '') {
      const rc = extractToolResultText(obj)
      return rc != null && rc.split('\n').length > COLLAPSED_RESULT_ROWS
    }

    return false
  })

  // Determine if this tool_result has a diff (Edit/Write with structuredPatch or old/new strings).
  const hasToolResultDiff = createMemo(() => {
    if (isCodexCompletedFileChangeResult()) {
      const item = parsed().parentObject?.item as Record<string, unknown> | undefined
      const changes = Array.isArray(item?.changes) ? item.changes as Array<Record<string, unknown>> : []
      return changes.some(change => typeof change.diff === 'string' && change.diff.includes('@@ '))
    }

    const info = toolResultInfo()
    if (!info)
      return false
    const { toolUseResult } = info
    if (!toolUseResult)
      return false
    if (Array.isArray(toolUseResult.structuredPatch) && (toolUseResult.structuredPatch as unknown[]).length > 0)
      return true
    const oldString = String(toolUseResult.oldString || '')
    const newString = String(toolUseResult.newString || '')
    return oldString !== '' && newString !== '' && oldString !== newString
  })

  // Extract copyable content for tool_result messages (Read/Write/Edit/Bash/etc.).
  const copyableResultContent = createMemo((): string | null => {
    if (isCodexTerminalCommandResult()) {
      const item = parsed().parentObject?.item as Record<string, unknown> | undefined
      return typeof item?.aggregatedOutput === 'string' ? item.aggregatedOutput : null
    }

    if (isCodexCompletedFileChangeResult()) {
      const item = parsed().parentObject?.item as Record<string, unknown> | undefined
      const changes = Array.isArray(item?.changes) ? item.changes as Array<Record<string, unknown>> : []
      const diffs = changes
        .map(change => typeof change.diff === 'string' ? change.diff : '')
        .filter(Boolean)
      return diffs.length > 0 ? diffs.join('\n\n') : null
    }

    const info = toolResultInfo()
    if (!info)
      return null

    const { obj, toolUseResult, toolName } = info

    // Edit: format as unified diff.
    if (toolName === 'Edit') {
      const structuredPatch = Array.isArray(toolUseResult?.structuredPatch) && (toolUseResult!.structuredPatch as unknown[]).length > 0
        ? toolUseResult!.structuredPatch as Array<{ oldStart: number, oldLines: number, newStart: number, newLines: number, lines: string[] }>
        : null
      const filePath = String(toolUseResult?.filePath || '')
      if (structuredPatch) {
        return formatUnifiedDiffText(structuredPatch, filePath)
      }
      const oldStr = String(toolUseResult?.oldString || '')
      const newStr = String(toolUseResult?.newString || '')
      if (oldStr && newStr && oldStr !== newStr) {
        return formatUnifiedDiffText(rawDiffToHunks(oldStr, newStr), filePath)
      }
      return null
    }

    // Read: copy file content from structured data.
    if (toolName === 'Read') {
      const file = toolUseResult?.file as Record<string, unknown> | undefined
      if (file && typeof file.content === 'string')
        return file.content
    }

    // Write: copy new file content from structured data.
    if (toolName === 'Write') {
      if (typeof toolUseResult?.newString === 'string')
        return toolUseResult.newString as string
    }

    // Fallback: extract raw result content for any tool.
    return extractToolResultText(obj)
  })

  const [resultCopied, setResultCopied] = createSignal(false)
  const copyResultContent = async () => {
    const content = copyableResultContent()
    if (!content)
      return
    await navigator.clipboard.writeText(content)
    setResultCopied(true)
    setTimeout(setResultCopied, 2000, false)
  }

  const diffView = () => localDiffView() ?? prefs.diffView()
  const toggleDiffView = () => setLocalDiffView(diffView() === 'unified' ? 'split' : 'unified')

  // Build render context for message renderers.
  const renderContext = (): RenderContext => ({
    createdAt: props.message.createdAt,
    workingDir: props.workingDir,
    homeDir: props.homeDir,
    diffView: diffView(),
    onCopyJson: copyJson,
    jsonCopied: jsonCopied(),
    toolUseMessage: toolUseMessage(),
    spanColor: props.message.spanColor,
    spanType: props.message.spanType,
    spanId: props.message.spanId,
    toolResultExpanded: toolResultExpanded(),
    commandStream: props.commandStream,
  })

  // Extract assistant text for the reply button.
  const extractAssistantText = (): string | null => {
    const cat = category()
    if (cat.kind !== 'assistant_text' && cat.kind !== 'assistant_thinking')
      return null
    const obj = parsed().parentObject
    if (!obj)
      return null

    // Codex format: {item: {type: 'agentMessage', text: '...'}, ...}
    const item = obj.item as Record<string, unknown> | undefined
    if (item?.type === 'agentMessage' && typeof item.text === 'string')
      return item.text.trim() || null

    // Claude Code format: {type: 'assistant', message: {content: [...]}}
    const content = getAssistantContent(obj)
    if (!content)
      return null
    return content
      .filter(c => c.type === 'text' || c.type === 'thinking')
      .map(c => String(c.type === 'thinking' ? (c as Record<string, unknown>).thinking || '' : c.text || ''))
      .join('\n')
      .trim() || null
  }

  // Extract user text for the quote button.
  const extractUserText = (): string | null => {
    const cat = category()
    if (cat.kind !== 'user_text' && cat.kind !== 'user_content')
      return null
    const obj = parsed().parentObject
    if (!obj)
      return null
    if (cat.kind === 'user_text') {
      const msg = obj.message as Record<string, unknown> | undefined
      if (typeof msg?.content === 'string')
        return msg.content.trim() || null
    }
    if (cat.kind === 'user_content' && typeof obj.content === 'string')
      return (obj.content as string).trim() || null
    return null
  }

  const extractQuotableText = () => extractAssistantText() ?? extractUserText()

  const handleReply = () => {
    const text = extractQuotableText()
    if (text && props.onReply) {
      props.onReply(formatChatQuote(text))
    }
  }

  const copyMarkdown = async () => {
    const text = extractQuotableText()
    if (!text)
      return
    await navigator.clipboard.writeText(text)
    setMarkdownCopied(true)
    setTimeout(setMarkdownCopied, 2000, false)
  }

  const rowClass = () => messageRowClass(category().kind, props.message.role)
  const isLocalPending = () => props.message.id.startsWith('local-')
  const isPendingUserMessage = () => isLocalPending() && props.message.role === MessageRole.USER && !props.error
  const bubbleClass = () => isPendingUserMessage()
    ? chatStyles.userMessagePending
    : messageBubbleClass(category().kind, props.message.role)

  onMount(() => {
    if (contentRef)
      injectCopyButtons(contentRef)
  })

  return (
    <div
      class={props.error ? styles.messageWithError : undefined}
      style={!props.error ? { display: 'contents' } : undefined}
    >
      <div class={rowClass()}>
        <div
          class={bubbleClass()}
          data-testid="message-bubble"
          data-role={roleLabel(props.message.role)}
        >
          <div ref={contentRef} data-testid="message-content">
            <ErrorBoundary fallback={renderErrorFallback('Failed to render message:')}>
              {category().kind === 'notification_thread'
                ? renderNotificationThread((category() as { kind: 'notification_thread', messages: unknown[] }).messages)
                : renderMessageContent(parsed().parentObject ?? parsed().rawText, props.message.role, renderContext(), category(), props.message.agentProvider)}
            </ErrorBoundary>
          </div>
        </div>
        <Show when={!hasInternalActions()}>
          <ToolHeaderActions
            createdAt={props.message.createdAt}
            onCopyJson={copyJson}
            jsonCopied={jsonCopied()}
            onCopyContent={copyableResultContent() ? copyResultContent : undefined}
            contentCopied={resultCopied()}
            expanded={toolResultExpanded()}
            onToggleExpand={isCollapsibleToolResult() ? () => setToolResultExpanded(v => !v) : undefined}
            hasDiff={hasToolResultDiff()}
            diffView={diffView()}
            onToggleDiffView={hasToolResultDiff() ? toggleDiffView : undefined}
            onReply={extractQuotableText() ? handleReply : undefined}
            onCopyMarkdown={extractQuotableText() ? copyMarkdown : undefined}
            markdownCopied={markdownCopied()}
          />
        </Show>
      </div>

      <Show when={props.error}>
        <div class={styles.messageError} data-testid="message-error">
          <span class={styles.messageErrorText}>Failed to deliver</span>
          <span class={styles.messageErrorDot}>&middot;</span>
          <button class={styles.messageRetryButton} onClick={() => props.onRetry?.()} data-testid="message-retry-button">Retry</button>
          <span class={styles.messageErrorDot}>&middot;</span>
          <button class={styles.messageDeleteButton} onClick={() => props.onDelete?.()} data-testid="message-delete-button">Delete</button>
        </div>
      </Show>
    </div>
  )
}
