import type { Component } from 'solid-js'
import type { RenderContext } from './messageRenderers'
import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'

import { Formatter, FracturedJsonOptions } from 'fracturedjsonjs'
import Check from 'lucide-solid/icons/check'
import Copy from 'lucide-solid/icons/copy'
import { createMemo, createSignal, ErrorBoundary, For, onMount, Show } from 'solid-js'
import { render } from 'solid-js/web'
import { IconButton } from '~/components/common/IconButton'
import { usePreferences } from '~/context/PreferencesContext'
import { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import { decompressContentToString } from '~/lib/decompress'
import * as styles from './MessageBubble.css'
import { classifyMessage, messageBubbleClass, messageRowClass } from './messageClassification'
import { renderMessageContent, ToolHeaderActions } from './messageRenderers'
import * as chatStyles from './messageStyles.css'
import { renderNotificationThread } from './notificationRenderers'

function roleLabel(role: MessageRole): string {
  switch (role) {
    case MessageRole.USER: return 'user'
    case MessageRole.ASSISTANT: return 'assistant'
    default: return 'system'
  }
}

/** Map a Claude Code JSON type field to a proto MessageRole. */
function typeToRole(type: string | undefined): MessageRole {
  switch (type) {
    case 'user': return MessageRole.USER
    case 'assistant': return MessageRole.ASSISTANT
    case 'system': return MessageRole.SYSTEM
    case 'result': return MessageRole.RESULT
    default: return MessageRole.UNSPECIFIED
  }
}

const formatter = new Formatter()
const opts = new FracturedJsonOptions()
opts.MaxTotalLineLength = 80
opts.MaxInlineComplexity = 1
formatter.Options = opts

function prettifyJson(raw: string): string {
  try {
    return formatter.Reformat(raw)
  }
  catch {
    return raw
  }
}

function injectCopyButtons(container: HTMLElement) {
  const preElements = container.querySelectorAll('pre')
  for (const pre of preElements) {
    if (pre.querySelector('.copy-code-button'))
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
              setTimeout(() => setCopied(false), 2000)
            })
          }}
        />
      )
    }, host)

    pre.appendChild(host)
  }
}

interface MessageBubbleProps {
  message: AgentChatMessage
  error?: string
  onRetry?: () => void
  onDelete?: () => void
  workingDir?: string
  homeDir?: string
}

export const MessageBubble: Component<MessageBubbleProps> = (props) => {
  const prefs = usePreferences()
  const [jsonCopied, setJsonCopied] = createSignal(false)
  const [threadExpandedManual, setThreadExpandedManual] = createSignal<boolean | null>(null)
  let contentRef: HTMLDivElement | undefined

  // Consolidated message parsing: decompress and parse once, derive everything from this memo.
  interface ParsedMessage {
    /** The wrapper envelope, or null if content is not wrapped. */
    wrapper: { old_seqs: number[], messages: unknown[] } | null
    /** The first (parent) message as a parsed object. */
    parentObject: Record<string, unknown> | undefined
    /** The raw decompressed text (for "Copy Raw JSON" feature). */
    rawText: string
    /** Thread children (messages after the first). */
    children: unknown[]
  }

  const parsed = createMemo((): ParsedMessage => {
    const text = decompressContentToString(props.message.content, props.message.contentCompression)
    if (text === null)
      return { wrapper: null, parentObject: undefined, rawText: '', children: [] }

    try {
      const obj = JSON.parse(text)
      if (obj?.messages && Array.isArray(obj.messages) && obj.messages.length > 0) {
        // Wrapped format: {"old_seqs": [...], "messages": [{...}, ...]}
        const first = obj.messages[0]
        const parent = (typeof first === 'object' && first !== null && !Array.isArray(first))
          ? first as Record<string, unknown>
          : undefined
        return {
          wrapper: obj,
          parentObject: parent,
          rawText: text,
          children: obj.messages.length > 1 ? obj.messages.slice(1) : [],
        }
      }
      // Not wrapped — treat the parsed object as the parent directly.
      const parent = (typeof obj === 'object' && obj !== null && !Array.isArray(obj))
        ? obj as Record<string, unknown>
        : undefined
      return { wrapper: null, parentObject: parent, rawText: text, children: [] }
    }
    catch {
      return { wrapper: null, parentObject: undefined, rawText: text, children: [] }
    }
  })

  // Single-pass classification: replaces ~15 individual boolean flags.
  const category = createMemo(() => classifyMessage(parsed().parentObject, parsed().wrapper))

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
    if (msg.updatedAt)
      envelope.updated_at = msg.updatedAt
    if (msg.deliveryError)
      envelope.delivery_error = msg.deliveryError
    if (p.wrapper && p.wrapper.old_seqs.length > 0)
      envelope.old_seqs = p.wrapper.old_seqs

    if (p.wrapper) {
      envelope.messages = p.wrapper.messages
      return JSON.stringify(envelope)
    }

    try {
      envelope.messages = [JSON.parse(p.rawText)]
      return JSON.stringify(envelope)
    }
    catch {
      return p.rawText
    }
  }

  // Extract parent tool_use name and input from the category (pre-extracted during classification).
  const parentToolInfo = (): { name: string, input: Record<string, unknown> } | null => {
    const cat = category()
    if (cat.kind !== 'tool_use')
      return null
    const input = cat.toolUse.input
    return {
      name: cat.toolName,
      input: (typeof input === 'object' && input !== null && !Array.isArray(input))
        ? input as Record<string, unknown>
        : {},
    }
  }

  /** Tools whose single-line results should be auto-expanded. */
  const AUTO_EXPAND_TOOLS = new Set(['Bash', 'Grep', 'Read', 'Write', 'Glob'])

  // Check if the tool result content is short enough to auto-expand (single line).
  const shouldAutoExpand = (): boolean => {
    const children = parsed().children
    if (children.length !== 1)
      return false
    // Only auto-expand for specific tool types
    const toolInfo = parentToolInfo()
    if (!toolInfo || !AUTO_EXPAND_TOOLS.has(toolInfo.name))
      return false
    try {
      const child = children[0] as Record<string, unknown>
      if (child?.type !== 'user')
        return false
      const msg = child.message as Record<string, unknown>
      if (!msg?.content || !Array.isArray(msg.content))
        return false
      const toolResult = (msg.content as Array<Record<string, unknown>>).find(c => c.type === 'tool_result')
      if (!toolResult)
        return false
      const resultData = toolResult as Record<string, unknown>
      const resultContent = Array.isArray(resultData.content)
        ? (resultData.content as Array<Record<string, unknown>>)
            .filter(c => c.type === 'text')
            .map(c => c.text)
            .join('')
        : String(resultData.content || '')
      // Auto-expand if the result is a single line (no newlines) and short
      return !resultContent.includes('\n') && resultContent.length > 0
    }
    catch { return false }
  }

  // Effective expanded state: manual toggle overrides auto-expand default.
  const threadExpanded = () => threadExpandedManual() ?? shouldAutoExpand()
  const setThreadExpanded = (updater: boolean | ((prev: boolean) => boolean)) => {
    const current = threadExpanded()
    const next = typeof updater === 'function' ? updater(current) : updater
    setThreadExpandedManual(next)
  }

  // Whether the message is rendered by a renderer that has its own internal ToolHeaderActions.
  const hasInternalActions = () => category().kind === 'tool_use' || category().kind === 'assistant_thinking'

  const copyJson = async () => {
    await navigator.clipboard.writeText(prettifyJson(rawJson()))
    setJsonCopied(true)
    setTimeout(() => setJsonCopied(false), 2000)
  }

  // Extract structuredPatch from a thread child (tool_result) for the parent tool_use diff.
  // Scans all children to skip synthetic control responses that may precede the tool_result.
  const childToolUseResult = (): Record<string, unknown> | null => {
    for (const raw of parsed().children) {
      const child = raw as Record<string, unknown>
      if (typeof child?.tool_use_result === 'object' && child.tool_use_result !== null)
        return child.tool_use_result as Record<string, unknown>
    }
    return null
  }

  // Extract text content from a thread child's tool_result.
  // Scans all children to skip synthetic control responses that may precede the tool_result.
  const extractChildResultContent = (): string | undefined => {
    for (const raw of parsed().children) {
      try {
        const child = raw as Record<string, unknown>
        if (child?.type !== 'user')
          continue
        const msg = child.message as Record<string, unknown>
        if (!msg?.content || !Array.isArray(msg.content))
          continue
        const toolResult = (msg.content as Array<Record<string, unknown>>).find(c => c.type === 'tool_result')
        if (!toolResult)
          continue
        const resultData = toolResult as Record<string, unknown>
        const content = Array.isArray(resultData.content)
          ? (resultData.content as Array<Record<string, unknown>>)
              .filter(c => c.type === 'text')
              .map(c => c.text)
              .join('')
          : (typeof resultData.content === 'string' ? resultData.content : '')
        if (content)
          return content
      }
      catch { continue }
    }
    return undefined
  }

  // Extract control response (approval/rejection) from thread children.
  const childControlResponse = (): { action: string, comment: string } | undefined => {
    const children = parsed().children
    for (const child of children) {
      const obj = child as Record<string, unknown>
      if (obj?.isSynthetic === true && typeof obj.controlResponse === 'object' && obj.controlResponse !== null) {
        const cr = obj.controlResponse as Record<string, unknown>
        return {
          action: String(cr.action || ''),
          comment: String(cr.comment || ''),
        }
      }
    }
    return undefined
  }

  // Thread children excluding the control response (for expanded thread display).
  const nonControlChildren = () =>
    parsed().children.filter((child) => {
      const obj = child as Record<string, unknown>
      return !(obj?.isSynthetic === true && typeof obj.controlResponse === 'object' && obj.controlResponse !== null)
    })

  // Build render context for message renderers.
  const renderContext = (): RenderContext => {
    const tur = childToolUseResult()
    return {
      createdAt: props.message.createdAt,
      updatedAt: props.message.updatedAt,
      workingDir: props.workingDir,
      homeDir: props.homeDir,
      threadChildCount: nonControlChildren().length,
      threadExpanded: threadExpanded(),
      onToggleThread: () => setThreadExpanded(prev => !prev),
      diffView: prefs.diffView(),
      onCopyJson: copyJson,
      jsonCopied: jsonCopied(),
      childStructuredPatch: Array.isArray(tur?.structuredPatch) ? tur!.structuredPatch as RenderContext['childStructuredPatch'] : undefined,
      childFilePath: typeof tur?.filePath === 'string' ? tur.filePath : undefined,
      childAnswers: (typeof tur?.answers === 'object' && tur.answers !== null && !Array.isArray(tur.answers))
        ? tur.answers as Record<string, string>
        : undefined,
      childResultContent: extractChildResultContent(),
      childControlResponse: childControlResponse(),
    }
  }

  const rowClass = () => messageRowClass(category().kind, props.message.role)
  const bubbleClass = () => messageBubbleClass(category().kind, props.message.role)

  onMount(() => {
    if (contentRef)
      injectCopyButtons(contentRef)
  })

  return (
    <Show when={category().kind !== 'hidden'}>
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
              <ErrorBoundary fallback={<span class={chatStyles.systemMessage}>Failed to render message</span>}>
                {category().kind === 'notification_thread'
                  ? renderNotificationThread((category() as { kind: 'notification_thread', messages: unknown[] }).messages)
                  : renderMessageContent(parsed().parentObject ?? parsed().rawText, props.message.role, renderContext(), category())}
              </ErrorBoundary>
            </div>
          </div>
          <Show when={!hasInternalActions()}>
            <ToolHeaderActions
              createdAt={props.message.createdAt}
              updatedAt={props.message.updatedAt}
              threadCount={nonControlChildren().length}
              threadExpanded={threadExpanded()}
              onToggleThread={() => setThreadExpanded(prev => !prev)}
              onCopyJson={copyJson}
              jsonCopied={jsonCopied()}
            />
          </Show>
        </div>

        {/* Thread children (e.g. tool_result after tool_use) — expanded via ThreadExpander in tool header */}
        <Show when={nonControlChildren().length > 0 && threadExpanded() && category().kind !== 'notification_thread'}>
          <div class={styles.threadChildren}>
            {(() => {
              const toolInfo = parentToolInfo()
              return (
                <For each={nonControlChildren()}>
                  {(child) => {
                    const childRole = () => typeToRole((child as Record<string, unknown>)?.type as string | undefined)
                    return (
                      <div
                        class={chatStyles.metaMessage}
                        data-testid="thread-child-bubble"
                      >
                        <ErrorBoundary fallback={<span class={chatStyles.systemMessage}>Failed to render message</span>}>
                          {renderMessageContent(child, childRole(), {
                            workingDir: props.workingDir,
                            homeDir: props.homeDir,
                            parentToolName: toolInfo?.name,
                            parentToolInput: toolInfo?.input,
                          })}
                        </ErrorBoundary>
                      </div>
                    )
                  }}
                </For>
              )
            })()}
          </div>
        </Show>

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
    </Show>
  )
}
