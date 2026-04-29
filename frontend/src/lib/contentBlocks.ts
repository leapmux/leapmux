/**
 * Shared helpers for Anthropic-style content-block arrays.
 *
 * Several agents (Claude, Pi, ACP-forwarded tool results, etc.) emit text
 * inside an array of content blocks shaped like
 *
 *     [{type: 'text', text}, {type: 'thinking', thinking},
 *      {type: 'image', data, mimeType}, {type: 'tool_use', name, input}, ...]
 *
 * Different providers attach this array to different envelope keys
 * (`message.content`, `result.content`, …) and use different block kinds.
 * The helpers below operate on the array itself, so each provider can
 * extract it from its own wire shape and then share the same text-joining
 * and filtering logic.
 */

import { isObject } from './jsonPick'

/** A content block in an Anthropic-style content array. */
export type ContentBlock = Record<string, unknown>

/**
 * Read `parent.message.content` as a content-block array. Returns null
 * when the envelope doesn't carry a nested `message.content` array — the
 * common shape for Claude/Pi assistant message envelopes.
 */
export function getMessageContent(parent: Record<string, unknown> | null | undefined): ContentBlock[] | null {
  if (!parent)
    return null
  const message = parent.message
  if (!isObject(message))
    return null
  const content = (message as Record<string, unknown>).content
  if (!Array.isArray(content))
    return null
  return content as ContentBlock[]
}

/** Narrow an unknown value to a content-block array. */
export function asContentArray(value: unknown): ContentBlock[] | null {
  return Array.isArray(value) ? (value as ContentBlock[]) : null
}

/**
 * Format a non-text block as a string for inclusion in a joined-text
 * output. Returning `null` skips the block (the historical default).
 *
 * The shipped {@link markdownImageFormatter} handles the image shapes
 * the providers in this repo emit; pass `() => null` to skip images
 * entirely (preserves prior silent-skip behavior).
 */
export type BlockFormatter = (block: ContentBlock) => string | null

/**
 * Default non-text formatter: render images as Markdown so they survive
 * in any text rendering context. Base64 image data becomes an inline
 * `![image](data:...)` so it embeds when the surrounding renderer is
 * Markdown-aware; an external URL becomes `[image](url)` (a link, not
 * an inline embed) so we don't trigger a fetch from a third-party host.
 *
 * Handles the three image shapes used in this repo:
 *   - Pi / MCP flat:   `{type:'image', mimeType, data}`
 *   - Anthropic:       `{type:'image', source: {type:'base64'|'url', media_type|url, ...}}`
 *   - MCP url-or-data: `{type:'image', mimeType?, urlOrData}`
 *
 * Returns null for non-image blocks and for image blocks whose shape
 * doesn't match any of the above (caller decides to skip or fall back).
 */
export const markdownImageFormatter: BlockFormatter = (block) => {
  if (block.type !== 'image')
    return null
  if (typeof block.data === 'string' && typeof block.mimeType === 'string')
    return `![image](data:${block.mimeType};base64,${block.data})`
  const source = isObject(block.source) ? block.source : null
  if (source) {
    if (source.type === 'base64'
      && typeof source.data === 'string'
      && typeof source.media_type === 'string') {
      return `![image](data:${source.media_type};base64,${source.data})`
    }
    if (source.type === 'url' && typeof source.url === 'string')
      return `[image](${source.url})`
  }
  if (typeof block.urlOrData === 'string') {
    const url = block.urlOrData
    return url.startsWith('data:') ? `![image](${url})` : `[image](${url})`
  }
  return null
}

/**
 * Walk content blocks in order, picking out the text in any block whose
 * `type` is a key of `kinds` (mapped to the field name to read) and any
 * block accepted by `formatOther` (default: {@link markdownImageFormatter}),
 * and concatenate with at least two newlines between non-empty entries.
 *
 * "At least two" handles content that already ends with newlines — we
 * only pad up to two, never trim down. So `"A\n"` + `"B"` produces
 * `"A\n\nB"`, but `"A\n\n\n"` + `"B"` is preserved as `"A\n\n\nB"`.
 *
 * Original block ordering is preserved: a `[text, thinking, text]`
 * sequence with `kinds = {text: 'text', thinking: 'thinking'}` produces
 * three paragraphs in input order.
 */
export function joinContentParagraphs(
  content: ContentBlock[] | null | undefined,
  kinds: Record<string, string>,
  formatOther: BlockFormatter = markdownImageFormatter,
): string {
  if (!content)
    return ''
  let out = ''
  const append = (chunk: string) => {
    if (chunk === '')
      return
    if (out !== '') {
      const trailing = (out.match(/\n*$/)?.[0] ?? '').length
      if (trailing < 2)
        out += '\n'.repeat(2 - trailing)
    }
    out += chunk
  }
  for (const block of content) {
    if (!isObject(block))
      continue
    const field = kinds[block.type as string]
    if (field) {
      const v = block[field]
      if (typeof v === 'string')
        append(v)
      continue
    }
    const formatted = formatOther(block)
    if (typeof formatted === 'string')
      append(formatted)
  }
  return out
}
