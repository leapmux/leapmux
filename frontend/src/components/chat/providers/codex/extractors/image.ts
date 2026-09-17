import type { ImageResultSource } from '~/lib/imageBlocks'
import { CODEX_ITEM } from '~/generated/contracts/codex-protocol'
import { pickString } from '~/lib/jsonPick'
import { fileUriToPath } from '~/lib/paths'

/** The one format Codex's image generation returns; it hardcodes the same. */
const CODEX_GENERATED_IMAGE_MIME = 'image/png'

/**
 * A Codex `path` field as a workspace path, or undefined when the URI does not parse.
 *
 * An `imageView` item sends a `file:` URI where every other item sends a plain path,
 * so both the image source and the read row strip the scheme HERE. A row that read
 * the raw field showed the URI where every other read row shows a plain path.
 *
 * The two callers want different answers for a URI that does not parse, which is why
 * this states undefined rather than choosing one: the image source drops it, because
 * the resolver cannot read a `file:` string, and the read row keeps the raw text,
 * because a reader gets more from it than from a blank path.
 */
export function codexItemPath(path: string): string | undefined {
  return path.startsWith('file:') ? fileUriToPath(path) : path
}

/** Codex supplies a file path for image views. The shared resolver supplies the pixels. */
export function codexViewedImage(item: Record<string, unknown> | null | undefined): ImageResultSource | null {
  if (item?.type !== CODEX_ITEM.ImageView)
    return null
  const filePath = codexItemPath(pickString(item, 'path'))
  return filePath ? { filePath } : null
}

/** The generated image an `imageGeneration` item carries, if it produced one. */
export function codexGeneratedImage(item: Record<string, unknown> | null | undefined): ImageResultSource | null {
  if (!item || item.type !== CODEX_ITEM.ImageGeneration)
    return null
  // `result` is empty while the item is in progress and after a failure.
  const data = pickString(item, 'result', undefined)?.trim()
  if (!data)
    return null
  const savedPath = pickString(item, 'savedPath', undefined)
  return savedPath
    ? { data, mimeType: CODEX_GENERATED_IMAGE_MIME, filePath: savedPath }
    : { data, mimeType: CODEX_GENERATED_IMAGE_MIME }
}
