/* eslint-disable solid/components-return-once -- render methods are not Solid components */
import type { JSX } from 'solid-js'
import type { MessageContentRenderer, RenderContext } from '../../messageRenderers'
import { joinContentParagraphs } from '~/lib/contentBlocks'
import { isObject } from '~/lib/jsonPick'
import { MarkdownText, PlanExecutionMessage, ThinkingMessage, UserContentMessage } from '../../messageRenderers'
import { getMessageContentArray } from './extractors/assistantContent'

/** Handles assistant messages: {"type":"assistant","message":{"content":[{"type":"text","text":"..."}]}} */
export const assistantTextRenderer: MessageContentRenderer = {
  render(parsed, context) {
    const content = getMessageContentArray(parsed)
    if (!content)
      return null
    const text = joinContentParagraphs(content, { text: 'text' })
    if (!text)
      return null
    return <MarkdownText text={text} context={context} />
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

/**
 * Handles user messages whose body is text: {"type":"user","message":{"content":...}}
 * with `content` either a plain string -- local slash command responses such as
 * /context -- or an array of text content blocks, which is the shape Claude
 * forwards into a SUBAGENT's own transcript (an interrupt notice, for one). If a
 * string is wrapped in <local-command-stdout> tags, the inner text is extracted.
 * Either way the result renders as markdown.
 */
export const userTextContentRenderer: MessageContentRenderer = {
  render(parsed, context) {
    if (!isObject(parsed) || parsed.type !== 'user')
      return null

    const message = parsed.message as Record<string, unknown>
    if (!isObject(message))
      return null

    const rawContent = message.content
    if (Array.isArray(rawContent)) {
      const joined = joinContentParagraphs(rawContent as Array<Record<string, unknown>>, { text: 'text' })
      return joined ? <MarkdownText text={joined} context={context} /> : null
    }
    const content = rawContent
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

    return <MarkdownText text={text} context={context} />
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
  render(parsed, context) {
    if (!isObject(parsed) || 'type' in parsed)
      return null
    return <UserContentMessage parsed={parsed} context={context} />
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
}
