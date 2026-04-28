/* eslint-disable solid/components-return-once -- render methods are not Solid components */
import type { JSX } from 'solid-js'
import type { MessageContentRenderer, RenderContext } from '../../messageRenderers'
import type { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import Bot from 'lucide-solid/icons/bot'
import FileIcon from 'lucide-solid/icons/file'
import FileImageIcon from 'lucide-solid/icons/file-image'
import { For, Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { isObject } from '~/lib/jsonPick'
import { MarkdownText, PlanExecutionMessage, ThinkingMessage } from '../../messageRenderers'
import { attachmentItem, attachmentList } from '../../messageStyles.css'
import { ToolStatusHeader } from '../../results/ToolStatusHeader'
import { getMessageContentArray, joinContentText } from './extractors/assistantContent'

/** Handles assistant messages: {"type":"assistant","message":{"content":[{"type":"text","text":"..."}]}} */
export const assistantTextRenderer: MessageContentRenderer = {
  render(parsed, _role, _context) {
    const content = getMessageContentArray(parsed)
    if (!content)
      return null
    const text = joinContentText(content)
    if (!text)
      return null
    return <MarkdownText text={text} />
  },
}

/** Handles assistant thinking messages: {"type":"assistant","message":{"content":[{"type":"thinking","thinking":"..."}]}} */
export const assistantThinkingRenderer: MessageContentRenderer = {
  render(parsed, _role, context) {
    const content = getMessageContentArray(parsed)
    if (!content)
      return null
    const text = joinContentText(content, 'thinking')
    if (!text)
      return null
    return <ThinkingMessage text={text} context={context} />
  },
}

/** Handles plan execution messages: {"content":"...","planExecution":true} */
export const planExecutionRenderer: MessageContentRenderer = {
  render(parsed, _role, context) {
    if (!isObject(parsed) || parsed.planExecution !== true)
      return null
    const content = parsed.content as string | undefined
    if (!content)
      return null
    return <PlanExecutionMessage text={content} context={context} />
  },
}

/** Renders task_started system messages as a minimal "Task started" line (thread child). */
export const taskStartedRenderer: MessageContentRenderer = {
  render(parsed, _role, _context) {
    if (!isObject(parsed) || parsed.type !== 'system' || parsed.subtype !== 'task_started')
      return null

    return <ToolStatusHeader icon={Bot} title="Task started" />
  },
}

/**
 * Handles user messages with string content: {"type":"user","message":{"content":"..."}}
 * This covers local slash command responses (e.g. /context) whose message.content
 * is a plain string rather than an array of content blocks. If the content is
 * wrapped in <local-command-stdout> tags, the inner text is extracted and rendered
 * as markdown.
 */
export const userTextContentRenderer: MessageContentRenderer = {
  render(parsed, _role, _context) {
    if (!isObject(parsed) || parsed.type !== 'user')
      return null

    const message = parsed.message as Record<string, unknown>
    if (!isObject(message))
      return null

    const content = message.content
    if (typeof content !== 'string')
      return null

    // Extract text between <local-command-stdout> tags if present.
    const startTag = '<local-command-stdout>'
    const endTag = '</local-command-stdout>'
    const startIdx = content.indexOf(startTag)
    const endIdx = content.indexOf(endTag)
    const text = startIdx !== -1 && endIdx !== -1 && endIdx > startIdx
      ? content.slice(startIdx + startTag.length, endIdx).trim()
      : content

    if (!text)
      return null

    return <MarkdownText text={text} />
  },
}

/** Handles user messages: {"content":"..."} or {"content":"...", "attachments":[...]} */
export const userContentRenderer: MessageContentRenderer = {
  render(parsed, _role, _context) {
    if (!isObject(parsed) || typeof parsed.content !== 'string' || 'type' in parsed)
      return null
    const attachments = Array.isArray((parsed as Record<string, unknown>).attachments)
      ? (parsed as Record<string, unknown>).attachments as Array<{ filename?: string, mime_type?: string }>
      : undefined
    const content = parsed.content as string
    const hasAttachments = attachments && attachments.length > 0
    const hasText = content.trim().length > 0

    if (!hasAttachments) {
      if (!hasText)
        return null
      return <MarkdownText text={content} />
    }

    return (
      <>
        <div class={attachmentList}>
          <For each={attachments}>
            {att => (
              <span class={attachmentItem}>
                <Icon
                  icon={att.mime_type?.startsWith('image/') ? FileImageIcon : FileIcon}
                  size="xs"
                />
                {att.filename ?? 'Unnamed file'}
              </span>
            )}
          </For>
        </div>
        <Show when={hasText}>
          <MarkdownText text={content} />
        </Show>
      </>
    )
  },
}

/**
 * Walk the Claude-shaped renderers in order, returning the first non-null
 * result. Used by the Claude plugin's `renderMessage` for the `'unknown'`
 * kind — the message classifier didn't recognize the shape, so each renderer
 * runs its own type-detection until one matches.
 */
export function tryClaudeUnknownKindRenderers(
  parsed: unknown,
  role: MessageRole,
  context: RenderContext | undefined,
): JSX.Element | null {
  return userTextContentRenderer.render(parsed, role, context)
    ?? assistantTextRenderer.render(parsed, role, context)
    ?? assistantThinkingRenderer.render(parsed, role, context)
    ?? userContentRenderer.render(parsed, role, context)
    ?? taskStartedRenderer.render(parsed, role, context)
}
