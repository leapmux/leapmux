import type { NumberedFileLine, ReadFileResult } from '../../../model/readFileResult'
import type { ReadRequest } from '../../../model/tools/read'
import type { ImageResultSource } from '~/lib/imageBlocks'
import { parseImageBlock, withFallbackFilePath } from '~/lib/imageBlocks'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

/**
 * Amp's `Read` tool.
 *
 *   {path, read_range?: [first, last]}  ->  {absolutePath, content}
 *
 * `content` numbers each line as `<n>: <text>`. A long file keeps its head and its
 * tail, with one `[... omitted lines A to B ...]` line between them. A directory
 * answers with `isDirectory: true` and one entry for each line, and an image answers
 * with its base64 data and `isImage: true`.
 */

const NUMBERED_LINE = /^(\d+): ?(.*)$/

/** The request one `Read` call states. The range is inclusive, and its lines count from 1. */
export function ampReadRequest(args: Record<string, unknown>): ReadRequest {
  const path = pickString(args, 'path')
  const range = Array.isArray(args.read_range) ? args.read_range : []
  const [first, last] = range
  if (typeof first !== 'number' || !Number.isInteger(first) || first < 1)
    return { path }
  const limit = typeof last === 'number' && Number.isInteger(last) && last >= first ? last - first + 1 : undefined
  return { path, offset: first, ...(limit !== undefined ? { limit } : {}) }
}

/**
 * The numbered lines of one `Read` body, or null when a line is not numbered.
 *
 * The omission line is not a line of the file, so a body that holds one draws as the
 * text Amp sent rather than as a numbered file with a gap no number states.
 */
function numberedLines(content: string): NumberedFileLine[] | null {
  const lines: NumberedFileLine[] = []
  for (const line of content.split('\n')) {
    const match = NUMBERED_LINE.exec(line)
    if (!match?.[1])
      return null
    lines.push({ num: Number(match[1]), text: match[2] ?? '' })
  }
  return lines
}

/** What one `Read` call returned: the file, and the picture when the file is one. */
export interface AmpReadOutcome {
  result: ReadFileResult
  images: ImageResultSource[]
}

/** The file one `Read` call returned, or null for a result that is not Amp's record. */
export function ampReadResult(text: string): AmpReadOutcome | null {
  let record: unknown
  try {
    record = JSON.parse(text)
  }
  catch {
    return null
  }
  if (!isObject(record) || typeof record.content !== 'string')
    return null
  const content = pickString(record, 'content')
  const path = pickString(record, 'absolutePath')
  // An image's content is its base64 data, which the row draws as the picture rather
  // than as text.
  if (record.isImage === true) {
    const image = parseImageBlock({ type: 'image', data: content, mimeType: pickString(pickObject(record, 'imageInfo'), 'mimeType') })
    return { result: { lines: null, fallbackContent: '' }, images: image ? [withFallbackFilePath(image, path || undefined)] : [] }
  }
  if (record.isDirectory === true)
    return { result: { lines: null, fallbackContent: content }, images: [] }
  if (content === '')
    return { result: { lines: [], fallbackContent: '' }, images: [] }
  return { result: { lines: numberedLines(content), fallbackContent: content }, images: [] }
}
