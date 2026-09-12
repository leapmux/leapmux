/**
 * The shared image parser supports each provider's content-block format.
 * Markdown formatting and MCP result rendering both use parseImageBlock.
 * A shared parser keeps their image support consistent, including Anthropic's nested source format.
 */

import type { ContentBlock } from './contentBlocks'
import type { ImageDimensions } from './imageDimensions'
import { isObject, pickString } from './jsonPick'
import { fileUriToPath } from './paths'

/** A normalized image from any provider's protocol. */
export interface ImageBlockSource {
  /** MIME type, when the provider supplies one. */
  mimeType?: string
  /** Base64 payload, WITHOUT the `data:<mime>;base64,` prefix. */
  data?: string
  /** A complete `data:` or `http(s):` URL. Mutually exclusive with `data`. */
  url?: string
  /**
   * The source file, when the provider supplies its path.
   * The viewer opens the full file through the worker when a path is available.
   * Otherwise, it opens the image bytes from the provider.
   */
  filePath?: string
}

/**
 * An image source with its provider-supplied dimensions and description.
 * Exact dimensions let the renderer reserve the image's space without decoding its header.
 * Claude supplies these dimensions in tool_use_result.file.dimensions.
 */
export type ImageResultSource = ImageBlockSource & { dimensions?: ImageDimensions, description?: string }

/**
 * Parse an image content block or an embedded image resource.
 * Return null for other content, including a resource that lacks image bytes.
 *
 * Explicit image blocks without data remain image sources with absent data and URL fields.
 * Anthropic file IDs and MIME-only MCP blocks need this behavior.
 * The renderer shows a no-data placeholder, and the image keeps its index for the image tab.
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
 *   - `{type:'resource', resource:{mimeType, blob}}` -- an embedded MCP image.
 *   - `{type:'image', mimeType?, url}` -- the MCP variant that points at a
 *     fetchable URL instead of inlining the bytes.
 *   - `{type:'image', mimeType?, urlOrData}` -- a normalized MCP image.
 *   - `{type:'inputImage', imageUrl}` -- Codex `dynamicToolCall.contentItems`.
 *   - `{type:'image', mediaType, dataUrl}` -- ZCode's internal part format.
 *     ZCode transcript recovery can supply native image data omitted from its event stream.
 */
export function parseImageBlock(block: ContentBlock): ImageBlockSource | null {
  if (!isObject(block))
    return null
  const type = block.type
  if (type === 'resource') {
    // Resource URIs belong to the MCP server and do not identify files on the worker.
    const resource = isObject(block.resource) ? block.resource : block
    const mimeType = pickString(resource, 'mimeType')
    return mimeType.toLowerCase().startsWith('image/') && typeof resource.blob === 'string'
      ? { data: resource.blob, mimeType }
      : null
  }
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

  // MCP, ACP, and Pi require a MIME type, but missing types still need an unsupported-format placeholder.
  // Accept complete data URLs in data. Otherwise, the renderer would prepend a second data-URL prefix.
  // A colon cannot occur in base64, so URL detection cannot misread a valid base64 payload.
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
    // File IDs and unknown source formats cannot supply image bytes here.
    // Keep the MIME type for the placeholder.
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
 * The list defines supported image formats alongside the shared size limit.
 * ImageRender supplies a blob URL to an img element.
 * That element prevents SVG scripts and external resource loads.
 *
 * Both transcript previews and the file viewer use ImageRender for SVG images.
 *
 * The raster-header decoder cannot determine SVG dimensions.
 * An SVG without supplied dimensions needs measurement after loading, which can adjust the scroll position.
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
 * The inline image limit counts base64 characters. Seven megabytes of base64 represent about five megabytes of raw data.
 * Transcript images and imageBlockToMarkdown share this limit.
 * Provider messages can exceed it, so each display path must check before it constructs an image URL or clipboard text.
 */
export const MAX_INLINE_IMAGE_BASE64_LEN = 7 * 1024 * 1024

/** Raw byte limit that corresponds to the shared inline image limit. */
export const MAX_FILE_IMAGE_BYTES = Math.floor(MAX_INLINE_IMAGE_BASE64_LEN / 4) * 3

/** True for the URL schemes the image renderer knows how to act on. */
function isRenderableUrl(value: string): boolean {
  return value.startsWith('data:') || value.startsWith('http://') || value.startsWith('https://')
}

/**
 * Fill an absent filePath from the caller's fallback.
 * The result's own path takes precedence because a tool can return a file other than the requested file.
 * The content parser and provider extractors share this rule.
 */
export function withFallbackFilePath<T extends ImageBlockSource>(source: T, filePath: string | undefined): T {
  return filePath && !source.filePath ? { ...source, filePath } : source
}

/**
 * Read a local path from an image block's file URI.
 * ACP providers can supply this URI on the image result.
 * Provider extractors resolve paths from tool inputs when the result omits them.
 */
function imageBlockFilePath(block: Record<string, unknown>): string | undefined {
  const uri = pickString(block, 'uri')
  return uri ? fileUriToPath(uri) : undefined
}

/** Why an image draws as a placeholder rather than inline. */
export type ImageSkipReason = 'no-data' | 'unsupported-mime' | 'too-large' | 'external-url' | 'unknown-shape'

/**
 * Split a `data:<mime>[;<param>...];base64,<payload>` URL.
 *
 * Both the transcript renderer and image viewer require a base64 parameter through this parser.
 * Otherwise, a URL could render inline but fail when the user opens its image tab.
 *
 * Return only the MIME type's essence: image/png for data:image/png;charset=utf-8;base64,...
 * Blob types and the format allowlist use that same value.
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
 * Determine whether an image can render inline, or give the reason it cannot.
 * Transcript rows and imageBlockToMarkdown share the same format and size checks.
 *
 *   - Inline base64 or a data URL requires a supported MIME type and an acceptable size.
 *   - An HTTP URL becomes an external link that the user must open.
 *   - Other values require a placeholder.
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
 * Apply the shared format and size limits to normalized MIME and base64 values.
 * Data URLs and separate data fields use the same checks.
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
 * Format an image for a quote, scroll-rail preview, or Markdown body.
 * Use imageRenderInfo so Markdown and transcript images share the same display policy.
 * Supported inline data becomes an image. External URLs become links that require user action.
 * Oversized images and unsupported formats produce text placeholders.
 * Missing data and unknown formats without a MIME type produce no Markdown.
 */
export function imageBlockToMarkdown(source: ImageBlockSource): string | null {
  const info = imageRenderInfo(source)
  if (info.src)
    return `![image](${info.src})`
  if (info.reason === 'external-url' && source.url)
    return `[image](${source.url})`
  // Preserve a text placeholder for an image that exceeds the inline size limit.
  if (info.reason === 'too-large')
    return source.mimeType ? `[image: ${source.mimeType} — too large to embed]` : '[image: too large to embed]'
  // Include unsupported formats only when the provider supplies a MIME type.
  if (info.reason === 'unsupported-mime' && source.mimeType)
    return `[image: ${source.mimeType} — unsupported format]`
  return null
}

/**
 * Format a placeholder for a destination that cannot display the image.
 * Include the MIME type when the provider supplies one.
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
