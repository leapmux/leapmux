import type { ImageResultSource } from '~/lib/imageBlocks'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { asContentArray, splitToolResultContent } from '~/lib/contentBlocks'
import { isObject, pickFirstNumber, pickObject, pickString } from '~/lib/jsonPick'
import { extractToolUseInfo, getMessageContentArray } from './assistantContent'

interface ClaudeImageArgs {
  /** The row's `tool_use_result` payload, when it carries one. */
  toolUseResult?: Record<string, unknown> | null
  /**
   * The image blocks already parsed out of the tool_result's `content[]` by
   * `splitToolResultContent`. Passed in rather than re-walked so the caller's
   * single pass over the blocks is the only one.
   */
  blockImages: ImageResultSource[]
  /** The paired tool_use's `input`, for the file path Claude states there. */
  toolInput?: Record<string, unknown> | null
}

/**
 * Every image a Claude tool_result carries, in wire order.
 *
 * Claude states an image twice for the tools that have a structured result.
 * A `Read` on a PNG sends the Anthropic block in `message.content[0].content[]`
 * AND a `tool_use_result` of
 * `{type:'image', file:{base64, type, originalSize, dimensions}}`. The
 * structured half is preferred because it carries `dimensions`: the renderer
 * can then reserve the image's final box without decoding a base64 header, so
 * a row measured off-screen keeps its height when the image decodes.
 *
 * Everything else -- an MCP server's images, a `Bash` whose stdout was a data
 * URI, a notebook cell output, a PDF page -- arrives only as content blocks.
 */
export function claudeImagesFromToolResult(args: ClaudeImageArgs): ImageResultSource[] {
  const filePath = pickString(args.toolInput, 'file_path', undefined)
  const withPath = (source: ImageResultSource): ImageResultSource =>
    filePath && !source.filePath ? { ...source, filePath } : source

  // The structured payload describes ONE image, so it can only stand in for
  // the blocks when the blocks are that same one. A result carrying several
  // images is never the `Read`-on-an-image shape this arm exists for, and
  // collapsing it to one would drop images the row renders -- and shift every
  // index an image tab addresses.
  const structured = args.blockImages.length <= 1 ? claudeStructuredImage(args.toolUseResult) : null
  if (structured)
    return [withPath(structured)]

  return args.blockImages.map(withPath)
}

/**
 * The `tool_use_result` image shape Claude Code's `Read` produces.
 *
 * `file.type` is the MIME type (`image/png`), NOT the `type: 'image'`
 * discriminant one field up. `dimensions` is optional -- older CLI versions
 * omit it -- so the renderer still falls back to sniffing when it is absent.
 */
function claudeStructuredImage(toolUseResult: Record<string, unknown> | null | undefined): ImageResultSource | null {
  if (!toolUseResult || toolUseResult.type !== 'image')
    return null
  const file = pickObject(toolUseResult, 'file')
  const data = pickString(file, 'base64', undefined)
  const mimeType = pickString(file, 'type', undefined)
  if (!data)
    return null
  const dimensions = claudeImageDimensions(file)
  return dimensions ? { data, mimeType, dimensions } : { data, mimeType }
}

/**
 * The intrinsic size of the base64 Claude actually sent.
 *
 * `dimensions` is `{originalWidth, originalHeight, displayWidth, displayHeight}`.
 * Claude downsamples an oversized image before it sends it, and the DISPLAY
 * pair describes the bytes we receive -- the CLI's own schema calls them
 * "image width in pixels (after resizing)". Reserving the box from the
 * ORIGINAL pair would be wrong for exactly the large screenshots the
 * reservation exists to keep from resizing a measured row.
 */
function claudeImageDimensions(file: Record<string, unknown> | null): { width: number, height: number } | null {
  const dims = pickObject(file, 'dimensions')
  if (!dims)
    return null
  const width = pickFirstNumber(dims, ['displayWidth', 'originalWidth', 'width'])
  const height = pickFirstNumber(dims, ['displayHeight', 'originalHeight', 'height'])
  // A zero or negative side would make the aspect-ratio reservation divide by
  // zero, so treat it the same as an absent field and sniff instead.
  return width && height && width > 0 && height > 0 ? { width, height } : null
}

/**
 * `Provider.toolResultImages` for Claude: every image in a tool_result row,
 * walked from the raw parsed message.
 *
 * Runs in two places -- the row renderer, and the image tab resolving index N
 * against the same message re-fetched from the worker -- so it takes the whole
 * message rather than pre-chewed pieces. The tab side has no paired tool_use
 * to read `file_path` from, which is why `toolUseParsed` is optional: it adds
 * the click target, never the order or the payload.
 */
export function claudeToolResultImages(
  parsed: unknown,
  _spanType: string | undefined,
  toolUseParsed: ParsedMessageContent | undefined,
): ImageResultSource[] {
  if (!isObject(parsed) || parsed.type !== 'user')
    return []
  const content = getMessageContentArray(parsed)
  if (!content)
    return []
  const toolResult = content.find(block => isObject(block) && block.type === 'tool_result')
  if (!toolResult)
    return []
  const blocks = asContentArray((toolResult as Record<string, unknown>).content)
  return claudeImagesFromToolResult({
    toolUseResult: pickObject(parsed, 'tool_use_result') ?? undefined,
    blockImages: blocks ? splitToolResultContent(blocks, { text: 'text' }).images : [],
    toolInput: toolUseParsed ? extractToolUseInfo(toolUseParsed)?.input : undefined,
  })
}
