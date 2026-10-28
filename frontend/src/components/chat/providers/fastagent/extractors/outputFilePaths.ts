import type { ToolCall } from '../../../model/toolCall'
import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { FASTAGENT_TOOL } from '~/generated/contracts/fastagent-protocol'
import { isObject } from '~/lib/jsonPick'
import { isFilesystemPath } from '~/lib/paths'
import { fastAgentArchiveSource } from '../archiveSource'
import { FAST_AGENT_SHELL } from '../protocol'

export function fastAgentOutputFilePaths(input: RowExtractionInput, call: ToolCall): readonly string[] {
  const source = fastAgentArchiveSource(input.resolved)
  if (!source || source.kind !== 'result' || input.span.role !== 'result' || source.spanId !== call.id
    || (input.spanId !== undefined && input.spanId !== source.spanId)
    || (source.request.name !== FASTAGENT_TOOL.Bash && source.request.name !== FASTAGENT_TOOL.Process)
    || !Array.isArray(source.result.content)) {
    return []
  }
  const paths = new Set<string>()
  for (const block of source.result.content) {
    if (!isObject(block) || block.type !== 'text' || typeof block.text !== 'string')
      continue
    for (const match of block.text.matchAll(FAST_AGENT_SHELL.OutputNotice)) {
      const path = match[1] ?? match[2]
      if (path && isFilesystemPath(path) && !path.includes('\0') && !path.includes('\r') && FAST_AGENT_SHELL.OutputPath.test(path))
        paths.add(path)
    }
  }
  return [...paths]
}
