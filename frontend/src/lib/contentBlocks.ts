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

import type { ImageResultSource } from './imageBlocks'
import { imageBlockToMarkdown, parseImageBlock } from './imageBlocks'
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
 * Default non-text formatter: render images as Markdown so they survive in any
 * text rendering context. See {@link imageBlockToMarkdown} for the embed-vs-link
 * rule and `~/lib/imageBlocks` for the wire shapes it accepts.
 *
 * This is the right default for a Markdown destination and the WRONG one for a
 * `<pre>`: there the data URL renders as a megabyte of literal base64. A
 * tool-result body that can mount a component calls
 * {@link splitToolResultContent} instead, which keeps the images out of the text
 * and hands them back as sources.
 *
 * Returns null for non-image blocks and for image blocks with no payload
 * (caller decides to skip or fall back).
 */
export const markdownImageFormatter: BlockFormatter = (block) => {
  const source = parseImageBlock(block)
  return source ? imageBlockToMarkdown(source) : null
}

/** A tool result split into the text to render and the images to mount. */
export interface ToolResultContent {
  /** The joined text, with every image block excluded. */
  text: string
  /**
   * Every image block, in wire order.
   *
   * The order is the contract behind an image tab's `imageIndex`: the row
   * renders index N and the tab resolves index N from the same message later,
   * so both must walk the blocks the same way.
   */
  images: ImageResultSource[]
}

/**
 * Split a content-block array into the joined text plus the image blocks.
 *
 * The text half is exactly what {@link joinContentParagraphs} produces with the
 * images skipped, so a caller that switches to this loses no text.
 */
export function splitToolResultContent(
  content: ContentBlock[] | null | undefined,
  kinds: Record<string, string>,
): ToolResultContent {
  // TWO walks, deliberately, and the image walk is its own.
  //
  // Collecting the images inside the text formatter looked cheaper and coupled
  // the list to the caller's `kinds`: `joinContentParagraphs` consults `kinds`
  // FIRST and never calls the formatter for a block whose type it names, so a
  // caller that named a type an image can also carry would drop that image from
  // the list AND from the text, with no error. This order is the contract an
  // image tab's `imageIndex` addresses by, so it must not depend on what a
  // caller happened to ask for the text.
  const images: ImageResultSource[] = []
  for (const block of content ?? []) {
    const source = parseImageBlock(block)
    if (source)
      images.push(source)
  }
  // The formatter answers null for every block: an image belongs to `images`,
  // and nothing else reaches here that the text should carry.
  const text = joinContentParagraphs(content, kinds, () => null)
  return { text, images }
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
