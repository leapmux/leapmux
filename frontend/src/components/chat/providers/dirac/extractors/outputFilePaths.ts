import type { ToolCall } from '../../../model/toolCall'
import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { DIRAC_TOOL } from '~/generated/contracts/dirac-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'
import { isFilesystemPath } from '~/lib/paths'
import { parsedACPToolCall } from '../../acp/extractors/toolCall'
import { DIRAC_OUTPUT_REFERENCE, DIRAC_RAW_INPUT_TOOL_FIELD } from '../protocol'

/** Read one complete final log pointer from the native output. */
export function diracOutputFilePointer(output: unknown): string | undefined {
  if (typeof output !== 'string')
    return undefined
  const lines = output.split('\n')
  const pointers = lines.filter(line => line.startsWith(DIRAC_OUTPUT_REFERENCE.TextPrefix))
  if (pointers.length !== 1 || lines.at(-1) !== pointers[0])
    return undefined
  const path = pointers[0]!.slice(DIRAC_OUTPUT_REFERENCE.TextPrefix.length)
  if (!isFilesystemPath(path) || path.includes('\r'))
    return undefined
  const parts = path.split(/[\\/]/u)
  const filename = parts.at(-1) ?? ''
  if (parts.at(-2) !== DIRAC_OUTPUT_REFERENCE.DirectoryName
    || !filename.startsWith(DIRAC_OUTPUT_REFERENCE.FilePrefix) || !filename.endsWith(DIRAC_OUTPUT_REFERENCE.FileExtension)) {
    return undefined
  }
  const identity = filename.slice(DIRAC_OUTPUT_REFERENCE.FilePrefix.length, -DIRAC_OUTPUT_REFERENCE.FileExtension.length)
  return /^\d+-[a-z0-9]+$/u.test(identity) ? path : undefined
}

/** Keep the command preview and expose only its declared log path. */
export function diracOutputFilePaths(input: RowExtractionInput, call: ToolCall): readonly string[] {
  const native = parsedACPToolCall(input.resolved.parentObject)
  if (input.span.role !== 'result' || !native || native.toolCallId !== call.id
    || (native.status !== 'completed' && native.status !== 'failed')
    || (pickString(pickObject(native, 'rawInput'), DIRAC_RAW_INPUT_TOOL_FIELD) || native.name || input.spanType || call.name) !== DIRAC_TOOL.ExecuteCommand) {
    return []
  }
  const path = diracOutputFilePointer(pickObject(native, 'rawOutput')?.output)
  return path === undefined ? [] : [path]
}
