import type { ContentBlock } from '~/lib/contentBlocks'
import { joinContentParagraphs } from '~/lib/contentBlocks'
import { prettifyJson } from '~/lib/jsonFormat'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

// ACP rawInput field aliases — agents emit camelCase, snake_case, or the
// short `path` form interchangeably. Extractors fall through these in order.
export const ACP_FILE_PATH_KEYS = ['filePath', 'path', 'file_path'] as const
export const ACP_OLD_TEXT_KEYS = ['oldText', 'oldString', 'old_string'] as const
export const ACP_NEW_TEXT_KEYS = ['newText', 'newString', 'new_string'] as const

/** Search targets can be one path or a native array of paths. */
export function acpInputPaths(input: Record<string, unknown>): string[] {
  for (const key of [...ACP_FILE_PATH_KEYS, 'paths']) {
    const value = input[key]
    const paths = Array.isArray(value) ? value.filter((entry): entry is string => typeof entry === 'string' && entry !== '') : typeof value === 'string' && value !== '' ? [value] : []
    if (paths.length > 0)
      return paths
  }
  return []
}

/**
 * Pull `rawOutput.metadata` out of an ACP tool_call_update. Several ACP
 * extractors (execute / search / webFetch) start by digging out this nested
 * shape; centralize it so the wire-format navigation lives in one place.
 */
export function pickAcpRawOutputMetadata(toolUse: Record<string, unknown> | null | undefined): Record<string, unknown> | null {
  return pickObject(pickObject(toolUse, 'rawOutput'), 'metadata')
}

/**
 * Flatten ACP's nested `[{type:'content', content:{...}}, ...]` shape into the
 * canonical Anthropic-style `[{type:'text', text}, ...]` so the shared
 * {@link splitToolResultContent} and `joinContentParagraphs` helpers handle ACP
 * content the same way they handle Claude/Pi/Codex content.
 *
 * The INNER block is unwrapped whatever its kind. Unwrapping only text used to
 * mean an ACP `ImageContent` -- what OpenCode's read-on-image, Kilo's and
 * Goose's screenshots all send, as
 * `{type:'content', content:{type:'image', mimeType, data}}` -- was dropped
 * here and never reached any renderer. Top-level entries that are not a
 * `content` wrapper (`diff`, `terminal`) pass through for their own extractors.
 */
export function flattenAcpContent(content: unknown): ContentBlock[] {
  if (!Array.isArray(content))
    return []
  return content.flatMap((item): ContentBlock[] => {
    if (!isObject(item))
      return []
    const entry = item as Record<string, unknown>
    if (entry.type === 'content' && isObject(entry.content)) {
      const inner = entry.content as Record<string, unknown>
      // Keyed on the `text` FIELD, not on `type: 'text'`: agents omit the
      // discriminant on a text block often enough that requiring it drops
      // ordinary command output.
      const text = pickString(inner, 'text')
      return text && (inner.type === undefined || inner.type === 'text') ? [{ type: 'text', text }] : [inner]
    }
    return [entry]
  })
}

/**
 * Pull joined text out of an ACP tool_call_update's `content[]`. Falls back
 * to `rawOutput.output || rawOutput.error` when the content array yields
 * nothing.
 *
 * Images are excluded: this text renders into a `<pre>`, where a data URL is a
 * megabyte of literal base64. `acpImagesFromToolCall` renders them as images.
 */
export function collectAcpToolText(toolUse: Record<string, unknown> | null | undefined, options: { rawObjects?: boolean } = {}): string {
  if (!toolUse)
    return ''
  const text = joinContentParagraphs(flattenAcpContent(toolUse.content), { text: 'text' }, () => null)
  if (text)
    return text
  const raw = toolUse.rawOutput
  const format = (value: unknown) => value === undefined || value === null
    ? ''
    : typeof value === 'object' ? prettifyJson(value) : String(value)
  if (!isObject(raw))
    return format(raw)
  if ('output' in raw || 'error' in raw)
    return [format(raw.output), format(raw.error)].filter(value => value !== '').join('\n')
  return options.rawObjects === false ? '' : format(raw)
}
