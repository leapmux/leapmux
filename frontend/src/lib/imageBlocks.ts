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
 * and still carry nothing -- an Anthropic `source:{type:'file'}` states a file
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
 * MIME types LeapMux renders inline.
 *
 * The list exists for the SIZE CAP and for one policy in one place, not as a
 * script defence. Every consumer mounts an image through `ImageRender`, which
 * builds a blob URL and hands it to an `<img>` -- and an `<img>` renders SVG in
 * SECURE STATIC MODE, where no script runs, no external resource loads and no
 * declarative animation plays. That is a property of the element, not of this
 * set.
 *
 * SVG is therefore included. Excluding it refused an agent's diagram while the
 * file viewer rendered the identical bytes off disk through the SAME
 * `ImageRender`, which is an inconsistency rather than a policy.
 *
 * One real cost: `sniffImageDimensionsFromDataUrl` reads intrinsic dimensions
 * from a raster header, and an SVG has none, so an SVG row reserves no box and
 * falls back to measure-on-load. That costs one scroll adjustment when it
 * decodes; it does not affect what is rendered.
 */
export const RENDERABLE_IMAGE_MIME_TYPES = new Set<string>([
  'image/png',
  'image/jpeg',
  'image/gif',
  'image/webp',
  'image/avif',
  'image/svg+xml',
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
  let pathname: string
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
  // A LITERAL `%` that is not valid percent-encoding makes decodeURIComponent
  // throw, and an agent that builds the uri by pasting a path after `file://`
  // sends one for any file named `50%off.png`. The raw pathname is the right
  // answer there: it is what the caller asked to open, and the only cost of
  // skipping the decode is that a genuinely encoded name stays encoded --
  // whereas returning undefined silently downgrades the click to the
  // transcript's downsampled copy, with nothing to explain why.
  try {
    return decodeURIComponent(pathname) || undefined
  }
  catch {
    return pathname || undefined
  }
}

/** Why an image draws as a placeholder rather than inline. */
export type ImageSkipReason = 'no-data' | 'unsupported-mime' | 'too-large' | 'external-url' | 'unknown-shape'

/**
 * Split a `data:<mime>[;<param>...];base64,<payload>` URL.
 *
 * ONE reader, because two disagreed. `imageRenderInfo` accepted a data URL with
 * no `;base64` and the image-tab decoder refused it, so such an image drew
 * inline with a click target and then opened a tab that said it could not
 * display the picture -- the branch that decoder's own comment calls
 * unreachable. Requiring `;base64` in the one parser is what makes it
 * unreachable in fact.
 *
 * `mimeType` is the essence alone: `data:image/png;charset=utf-8;base64,...`
 * answers `image/png`. A parameter that reached a Blob type made the tab and the
 * row describe the same bytes two ways, and the allowlist below is keyed on the
 * essence.
 */
export function parseDataImageUrl(url: string | undefined): { mimeType: string, base64: string } | null {
  if (!url?.startsWith('data:'))
    return null
  const comma = url.indexOf(',')
  if (comma < 0)
    return null
  const params = url.slice('data:'.length, comma).split(';')
  const mimeType = (params.shift() ?? '').toLowerCase()
  if (!params.some(param => param.toLowerCase() === 'base64'))
    return null
  return { mimeType, base64: url.slice(comma + 1) }
}

/**
 * Whether an image source draws inline, and if not, why.
 *
 * The single render policy for every destination. It lives beside the parser
 * rather than in the transcript row, because the row is not the only consumer:
 * `imageBlockToMarkdown` feeds every quote, preview and Markdown body, and it
 * used to embed a 50 MB payload that the row next to it refused.
 *
 *   - inline base64 + allowlisted MIME + under the cap -> `<img src="data:...">`
 *   - an already-formed `data:` URL, on the same terms
 *   - an http(s) URL -> not drawn; the caller shows an open link, so the
 *     transcript fetches from no remote host on its own
 *   - anything else -> a placeholder, so the reader still learns an image came
 *     back
 */
export function imageRenderInfo(source: ImageBlockSource): {
  src?: string
  via?: 'inline'
  reason?: ImageSkipReason
} {
  const url = source.url
  if (url) {
    if (url.startsWith('data:')) {
      const parsed = parseDataImageUrl(url)
      if (!parsed)
        return { reason: 'unknown-shape' }
      return renderPolicy(parsed.mimeType, parsed.base64, url)
    }
    // An http(s) URL is shown as an opt-in external link by the caller.
    if (url.startsWith('http://') || url.startsWith('https://'))
      return { reason: 'external-url' }
    return { reason: 'unknown-shape' }
  }

  const data = source.data
  if (!data)
    return { reason: 'no-data' }
  const mimeType = (source.mimeType ?? '').toLowerCase()
  return renderPolicy(mimeType, data, `data:${mimeType};base64,${data}`)
}

/**
 * The allowlist and the cap, applied once. Both shapes above normalize to
 * (mime, base64) first, so neither can be checked against a different length
 * than the other renders.
 */
function renderPolicy(mimeType: string, base64: string, src: string): {
  src?: string
  via?: 'inline'
  reason?: ImageSkipReason
} {
  if (!RENDERABLE_IMAGE_MIME_TYPES.has(mimeType))
    return { reason: 'unsupported-mime' }
  if (base64.length > MAX_INLINE_IMAGE_BASE64_LEN)
    return { reason: 'too-large' }
  return { src, via: 'inline' }
}

/**
 * Render an image block as Markdown, for the text-only contexts that have no
 * place to mount a component (a quote, a scroll-rail preview, a Markdown body).
 *
 * A renderable image becomes an inline `![image](data:...)`, which embeds when
 * the surrounding renderer is Markdown-aware. An external URL becomes a
 * `[image](url)` LINK, not an embed, so rendering it never fetches from a
 * third-party host.
 *
 * It asks `imageRenderInfo`, the SAME policy the transcript row asks, so the two
 * destinations can never disagree about which pictures are safe to embed. This
 * path used to skip that policy entirely, and `rehypeBlockRemoteImages` lets a
 * `data:` src through -- so an `image/svg+xml` or a 50 MB payload embedded here
 * after the row beside it had already refused to draw it.
 *
 * Anything the policy refuses returns null and the caller omits the block,
 * which is what this function has always done for a shape it could not render.
 * The row is where a refused image still leaves a visible trace, through
 * {@link imageSkipPlaceholder}; a Markdown body has no box to put one in.
 */
export function imageBlockToMarkdown(source: ImageBlockSource): string | null {
  const info = imageRenderInfo(source)
  if (info.src)
    return `![image](${info.src})`
  if (info.reason === 'external-url' && source.url)
    return `[image](${source.url})`
  // A refused image still leaves a trace, so a reader learns one came back.
  if (info.reason === 'too-large')
    return source.mimeType ? `[image: ${source.mimeType} — too large to embed]` : '[image: too large to embed]'
  // `unsupported-mime` says something only when a type was actually stated --
  // an `image/svg+xml`, which the allowlist refuses and this path used to
  // embed. Without a type the block carries nothing to describe, and this
  // function has always omitted such a block; so has `no-data`.
  if (info.reason === 'unsupported-mime' && source.mimeType)
    return `[image: ${source.mimeType} — unsupported format]`
  return null
}

/**
 * The text a destination shows in place of an image it will not draw.
 *
 * One wording for the transcript row and for the Markdown path, so a reader who
 * sees the same refused image in a quote and in the transcript reads the same
 * sentence. The MIME type is included when the wire carried one, because
 * "unsupported format" without the format is a question rather than an answer.
 */
export function imageSkipPlaceholder(reason: ImageSkipReason | undefined, mimeType?: string): string {
  const suffix = mimeType ? `: ${mimeType}` : ''
  switch (reason) {
    case 'too-large':
      return `[image${suffix} — too large to render inline]`
    case 'unsupported-mime':
      return `[image${suffix} — unsupported format]`
    default:
      return `[image${suffix}]`
  }
}
