import type { ToolCall } from '../../../model/toolCall'
import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { isObject, pickString } from '~/lib/jsonPick'
import { isFilesystemPath } from '~/lib/paths'
import { parsedACPToolCall } from '../../acp/extractors/toolCall'

const OUTPUT_PREFIX = 'Full output: '
const OUTPUT_INSTRUCTIONS = 'Read a window of it with read_file (offset/limit), or search it with grep. It is not in this conversation.'

/** Read Reasonix's native spill receipt and bind its file to this exact tool call. */
export function reasonixOutputFilePointer(text: unknown, toolName: string, callId: string): string | undefined {
  if (typeof text !== 'string' || !toolName || !callId)
    return undefined
  const [header, location, instructions] = text.split('\n', 3)
  const prefix = `[${toolName} output kept out of context: `
  if (!header?.startsWith(prefix) || !/^[1-9]\d* lines, [^\]\r\n]+\]$/.test(header.slice(prefix.length))
    || !location?.startsWith(OUTPUT_PREFIX) || instructions !== OUTPUT_INSTRUCTIONS) {
    return undefined
  }
  const path = location.slice(OUTPUT_PREFIX.length)
  if (!isFilesystemPath(path) || path.includes('\r'))
    return undefined
  // Reasonix's spillFileName replaces each non-ASCII filename character, trims hyphens,
  // and caps the call identity at 96 characters before adding the extension.
  const identity = callId.replace(/[^\w-]/gu, '-').replace(/^-+|-+$/g, '').slice(0, 96) || 'output'
  return path.split(/[\\/]/u).at(-1) === `${identity}.txt` ? path : undefined
}

/** Expose the saved output beside its original preview, without reading or replacing that preview. */
export function reasonixOutputFilePaths(input: RowExtractionInput, call: ToolCall): readonly string[] {
  const native = parsedACPToolCall(input.resolved.parentObject)
  if (input.span.role !== 'result' || !native || native.toolCallId !== call.id
    || (native.status !== 'completed' && native.status !== 'failed') || !Array.isArray(native.content)) {
    return []
  }
  const request = parsedACPToolCall(input.span.request?.parentObject)
  const nativeName = pickString(native, 'name') || pickString(native, 'title')
    || (request?.toolCallId === call.id ? pickString(request, 'name') || pickString(request, 'title') : '') || call.name
  const paths = native.content.flatMap((block: unknown) => {
    if (!isObject(block) || block.type !== 'content' || !isObject(block.content) || block.content.type !== 'text')
      return []
    const path = reasonixOutputFilePointer(block.content.text, nativeName, call.id)
    return path === undefined ? [] : [path]
  })
  return paths.length === 1 ? paths : []
}
