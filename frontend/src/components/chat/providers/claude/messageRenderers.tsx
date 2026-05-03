/* eslint-disable solid/components-return-once -- render methods are not Solid components */
import type { JSX } from 'solid-js'
import type { MessageContentRenderer, RenderContext } from '../../messageRenderers'
import Bot from 'lucide-solid/icons/bot'
import { joinContentParagraphs } from '~/lib/contentBlocks'
import { isObject } from '~/lib/jsonPick'
import { MarkdownText, PlanExecutionMessage, ThinkingMessage, UserContentMessage } from '../../messageRenderers'
import { ToolStatusHeader } from '../../results/ToolStatusHeader'
import { getMessageContentArray } from './extractors/assistantContent'

/** Handles assistant messages: {"type":"assistant","message":{"content":[{"type":"text","text":"..."}]}} */
export const assistantTextRenderer: MessageContentRenderer = {
  render(parsed, _context) {
    const content = getMessageContentArray(parsed)
    if (!content)
      return null
    const text = joinContentParagraphs(content, { text: 'text' })
    if (!text)
      return null
    return <MarkdownText text={text} />
  },
}

/** Handles assistant thinking messages: {"type":"assistant","message":{"content":[{"type":"thinking","thinking":"..."}]}} */
export const assistantThinkingRenderer: MessageContentRenderer = {
  render(parsed, context) {
    const content = getMessageContentArray(parsed)
    if (!content)
      return null
    const text = joinContentParagraphs(content, { thinking: 'thinking' })
    if (!text)
      return null
    return <ThinkingMessage text={text} context={context} />
  },
}

/** Handles plan execution messages: {"content":"...","planExecution":true} */
export const planExecutionRenderer: MessageContentRenderer = {
  render(parsed, context) {
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
  render(parsed, _context) {
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
  render(parsed, _context) {
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

/**
 * Handles Claude user messages: {"content":"..."} or
 * {"content":"...", "attachments":[...]}. Delegates to the shared
 * UserContentMessage so attachment rendering stays consistent across
 * providers, and adds Claude's discriminator: skip when the parsed body has
 * a `type` field (those are routed to other Claude-shaped renderers).
 */
export const userContentRenderer: MessageContentRenderer = {
  render(parsed, _context) {
    if (!isObject(parsed) || 'type' in parsed)
      return null
    return <UserContentMessage parsed={parsed} />
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
  context: RenderContext | undefined,
): JSX.Element | null {
  return userTextContentRenderer.render(parsed, context)
    ?? assistantTextRenderer.render(parsed, context)
    ?? assistantThinkingRenderer.render(parsed, context)
    ?? userContentRenderer.render(parsed, context)
    ?? taskStartedRenderer.render(parsed, context)
}
