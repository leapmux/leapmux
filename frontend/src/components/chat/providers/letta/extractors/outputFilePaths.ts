import type { ToolCall } from '../../../model/toolCall'
import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { LETTA_DELTA_KIND } from '~/generated/contracts/letta-protocol'
import { isFilesystemPath } from '~/lib/paths'
import { isLettaToolProgress, lettaReturnedData, lettaToolPayload } from '../toolOutput'

/** Read a native file footer only from a genuine final return for this call. */
export function lettaOutputFilePaths(input: RowExtractionInput, call: ToolCall): readonly string[] {
  const source = lettaToolPayload(input.resolved.parentObject)
  if (input.span.role !== 'result' || !source || source.message_type !== LETTA_DELTA_KIND.ToolReturnMessage
    || isLettaToolProgress(source) || source.tool_call_id !== call.id || typeof source.run_id !== 'string' || !source.run_id) {
    return []
  }
  const returned = lettaReturnedData(source)
  if (returned.kind !== 'present' || typeof returned.value !== 'string')
    return []
  const pointers = [...returned.value.matchAll(/^\[Full output written to: ([^\r\n]+)\]$/gmu)]
  const path = pointers.length === 1 ? pointers[0]?.[1] : undefined
  if (!isFilesystemPath(path) || !/\[Output truncated: showing [\d,]+ of [\d,]+ characters\.\]/u.test(returned.value))
    return []
  const parts = path.split(/[\\/]/u)
  return parts.at(-2) === 'agent-tools' && parts.at(-4) === 'projects' && parts.at(-5) === '.letta'
    && /^[a-z][a-z0-9_-]*-[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}\.txt$/u.test(parts.at(-1) ?? '')
    ? [path]
    : []
}
