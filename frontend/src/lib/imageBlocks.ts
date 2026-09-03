/**
 * The one parser for an image content block, across every agent provider.
 *
 * Each provider states an image in its own shape, and LeapMux renders all of
 * them the same way, so the wire-shape knowledge lives here instead of being
 * re-derived at each render site. Two consumers used to read the same block
 * and disagree -- `markdownImageFormatter` understood the Anthropic `source`
 * shape and `parseMcpContentItem` did not -- so a Claude MCP tool that
 * returned an Anthropic-shaped image rendered as a bare `[image]`. Both now
 * read {@link parseImageBlock}.
 */

import type { ContentBlock } from './contentBlocks'
import type { ImageDimensions } from './imageDimensions'
import { isObject, pickString } from './jsonPick'

/** A normalized image, however the provider spelled it on the wire. */
export interface ImageBlockSource {
  /** MIME type, when the wire carried one. */
  mimeType?: string
  /** Base64 payload, WITHOUT the `data:<mime>;base64,` prefix. */
  data?: string
  /** A complete `data:` or `http(s):` URL. Mutually exclusive with `data`. */
  url?: string
  /**
   * The file this image was read from, when the provider states one. Drives
   * the click target: with a path the viewer opens the file itself (full
   * resolution, straight off the worker); without one it opens the bytes the
   * agent sent.
   */
  filePath?: string
}

/**
 * An image ready to render: the wire block, plus the intrinsic size when the
 * provider states it.
 *
 * Stating the size is worth a field of its own because it is exact and free.
 * Claude's `tool_use_result.file.dimensions` sits beside the payload, so the
 * renderer can reserve the final box without decoding a base64 header, and a
 * row measured off-screen never resizes when the image later decodes.
 */
export type ImageResultSource = ImageBlockSource & { dimensions?: ImageDimensions }

/**
 * Parse one content block into an {@link ImageBlockSource}. Returns null only
 * when the block is not an image block at all.
 *
 * An image block with no renderable payload yields a source whose `data` and
 * `url` are both absent, rather than null. A block can be a legitimate image
 * and still carry nothing -- an Anthropic `source:{type:'file'}` names a file
 * on Anthropic's servers, and an MCP server may state a MIME type with no
 * payload. Keeping it lets the renderer say "an image was returned, and here
 * is why you cannot see it" (`imageRenderInfo` answers `no-data`) instead of
 * dropping the block without a trace -- and it keeps that block's INDEX, which
 * an image tab addresses by.
 *
 * The shapes, and who emits each:
 *
 *   - `{type:'image', source:{type:'base64', media_type, data}}` -- Anthropic.
 *     Claude Code's `Read` on an image, its MCP bridge, notebook cell outputs
 *     and PDF page images all land here, as does any tool_result forwarded
 *     verbatim from the Messages API.
 *   - `{type:'image', source:{type:'url', url}}` -- Anthropic, URL variant.
 *   - `{type:'image', data, mimeType}` -- the MCP content shape, which ACP's
 *     `ImageContent` and Pi's `ImageContent` both reuse verbatim.
 *   - `{type:'image', mimeType?, url}` -- the MCP variant that points at a
 *     fetchable URL instead of inlining the bytes.
 *   - `{type:'image', mimeType?, urlOrData}` -- the already-normalized MCP
 *     shape `parseMcpContentItem` produces.
 *   - `{type:'inputImage', imageUrl}` -- Codex `dynamicToolCall.contentItems`.
 *   - `{type:'image', mediaType, dataUrl}` -- ZCode's internal part shape.
 *     ZCode's app-server text-ifies images before they reach LeapMux, so this
 *     branch is defensive: it costs two lines and means a future app-server that
 *     forwards the part renders instead of printing a data URL.
 */
export function parseImageBlock(block: ContentBlock): ImageBlockSource | null {
  if (!isObject(block))
    return null
  const type = block.type
  if (type !== 'image' && type !== 'inputImage')
    return null

  const filePath = imageBlockFilePath(block)
  const withPath = (source: ImageBlockSource): ImageBlockSource =>
    withFallbackFilePath(source, filePath)

  // Codex `dynamicToolCall` content item: a URL, usually already a data URL.
  if (type === 'inputImage') {
    const imageUrl = pickString(block, 'imageUrl', undefined)
    return withPath(imageUrl ? { url: imageUrl } : {})
  }

  const mimeType = pickString(block, 'mimeType', undefined)

  // MCP / ACP / Pi flat shape. `mimeType` is required by all three specs but
  // read separately, so a payload with no type still reaches the renderer and
  // surfaces as "unsupported format" rather than vanishing.
  // `data` is base64 by spec, but a server that puts a whole `data:` URL there is
  // common enough that the old reader sniffed for it. Without the sniff the
  // renderer builds `data:<mime>;base64,data:<mime>;base64,...` -- a broken image
  // with no placeholder, and a tab whose decode throws. `:` is not in the base64
  // alphabet, so this can never misread a real payload.
  const data = pickString(block, 'data', undefined)
  if (data)
    return withPath(isRenderableUrl(data) ? { url: data, mimeType } : { data, mimeType })

  // MCP `url` variant: a server may state a fetchable URL instead of inlining.
  const url = pickString(block, 'url', undefined)
  if (url)
    return withPath({ url, mimeType })

  // ZCode part shape.
  const dataUrl = pickString(block, 'dataUrl', undefined)
  if (dataUrl)
    return withPath({ url: dataUrl, mimeType: mimeType ?? pickString(block, 'mediaType', undefined) })

  // Anthropic nested shape.
  const source = isObject(block.source) ? block.source : null
  if (source) {
    if (source.type === 'base64') {
      const b64 = pickString(source, 'data', undefined)
      const mediaType = pickString(source, 'media_type', undefined)
      if (b64)
        return withPath({ data: b64, mimeType: mediaType })
    }
    if (source.type === 'url') {
      const url = pickString(source, 'url', undefined)
      if (url)
        return withPath({ url })
    }
    // `source:{type:'file', file_id}` and anything else nested: an image we
    // cannot fetch. Keep the MIME type so the placeholder can name it.
    return withPath({ mimeType })
  }

  // Already-normalized MCP shape (`urlOrData` holds either a URL or bare base64).
  const urlOrData = pickString(block, 'urlOrData', undefined)
  if (urlOrData)
    return withPath(isRenderableUrl(urlOrData) ? { url: urlOrData, mimeType } : { data: urlOrData, mimeType })

  return withPath({ mimeType })
}

/**
 * MIME types LeapMux renders inline. SVG is excluded deliberately: an SVG can
 * carry script, and although a browser refuses to run it inside `<img>`, the
 * allowlist is what keeps that guarantee from resting on the mount point a
 * future caller happens to pick.
 */
export const RENDERABLE_IMAGE_MIME_TYPES = new Set<string>([
  'image/png',
  'image/jpeg',
  'image/gif',
  'image/webp',
  'image/avif',
])

/**
 * Cap on the base64-encoded length of an inline image. 7 MB base64 is about
 * 5 MB raw, which comfortably covers a screenshot.
 *
 * It lives here, beside {@link parseImageBlock}, because BOTH consumers of a
 * parsed source have to honour it: the component that mounts an `<img>`, and
 * {@link imageBlockToMarkdown}, which builds a data URL for a text destination.
 * The wire allows a far larger message than this cap, so an uncapped path can
 * put megabytes of base64 into an `src` attribute or a clipboard.
 */
export const MAX_INLINE_IMAGE_BASE64_LEN = 7 * 1024 * 1024

/** True for the URL schemes the image renderer knows how to act on. */
function isRenderableUrl(value: string): boolean {
  return value.startsWith('data:') || value.startsWith('http://') || value.startsWith('https://')
}

/**
 * Fill `filePath` from the caller's fallback, unless the block already stated
 * one.
 *
 * The block's own path always wins. It names the file the agent actually read,
 * where a fallback names only the file the tool was ASKED for -- and a tool that
 * was asked for one file and returned another is exactly the case the two can
 * disagree on.
 *
 * Every provider that can carry an image needs this same merge, and each one
 * spelled it differently until they shared it: `parseImageBlock` for an
 * in-band `file://` uri, and the Claude, ACP and Pi extractors for a path that
 * the tool INPUT states rather than the result.
 */
export function withFallbackFilePath<T extends ImageBlockSource>(source: T, filePath: string | undefined): T {
  return filePath && !source.filePath ? { ...source, filePath } : source
}

/**
 * The file an image block points at, when it names one.
 *
 * ACP's `ImageContent.uri` is the only in-band carrier -- a `file://` URL that
 * OpenCode, Kilo and Goose set when the image came off disk. Every other
 * provider states the path on the tool INPUT rather than the result, so its
 * extractor supplies `filePath` itself.
 */
function imageBlockFilePath(block: Record<string, unknown>): string | undefined {
  const uri = pickString(block, 'uri', undefined)
  if (!uri || !uri.startsWith('file://'))
    return undefined
  try {
    const parsed = new URL(uri)
    const path = decodeURIComponent(parsed.pathname)
    // `pathname` alone is not a local path on two shapes a worker really sends.
    // A Windows `file:///C:/x` gives `/C:/x`: the URL API keeps the slash before
    // the drive letter, and the worker cannot resolve that. A UNC
    // `file://server/share/x` puts `server` in the HOST, which the pathname drops
    // entirely. Node's `fileURLToPath` is not the answer -- it resolves against the
    // platform this code runs on, and the path belongs to the worker's platform.
    const host = decodeURIComponent(parsed.host)
    if (host)
      return `//${host}${path}` || undefined
    return (/^\/[a-z]:/i.test(path) ? path.slice(1) : path) || undefined
  }
  catch {
    return undefined
  }
}

/**
 * Render an image block as Markdown, for the text-only contexts that have no
 * place to mount a component (a quote, a scroll-rail preview, a Markdown body).
 *
 * Base64 becomes an inline `![image](data:...)`, which embeds when the
 * surrounding renderer is Markdown-aware. An external URL becomes a
 * `[image](url)` LINK, not an embed, so rendering it never fetches from a
 * third-party host.
 */
export function imageBlockToMarkdown(source: ImageBlockSource): string | null {
  if (source.data && source.mimeType) {
    if (source.data.length > MAX_INLINE_IMAGE_BASE64_LEN)
      return `[image: ${source.mimeType} — too large to embed]`
    return `![image](data:${source.mimeType};base64,${source.data})`
  }
  const url = source.url
  if (!url)
    return null
  if (!url.startsWith('data:'))
    return `[image](${url})`
  // The same cap on an already-formed data URL. Measured past the comma, so it
  // compares the payload and not the `data:<mime>;base64,` preamble.
  const comma = url.indexOf(',')
  if (comma >= 0 && url.length - comma - 1 > MAX_INLINE_IMAGE_BASE64_LEN)
    return '[image: too large to embed]'
  return `![image](${url})`
}
