import type { ToolCall } from '../../../model/toolCall'
import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { isObject } from '~/lib/jsonPick'
import { isFilesystemPath } from '~/lib/paths'
import { parsedACPToolCall } from '../../acp/extractors/toolCall'
import { FAST_AGENT_SHELL } from '../protocol'
import { fastAgentArchiveTool } from '../toolIdentity'

/** Expose only file pointers stated by this completed native shell result. */
export function fastAgentOutputFilePaths(input: RowExtractionInput, call: ToolCall): readonly string[] {
  const tool = parsedACPToolCall(input.resolved.parentObject)
  if (input.span.role !== 'result' || !tool || tool.toolCallId !== call.id
    || (tool.status !== 'completed' && tool.status !== 'failed')
    || fastAgentArchiveTool(tool, input.resolved.agentSessionId) === undefined || !isObject(tool.rawOutput)
    || !Array.isArray(tool.rawOutput.content)) {
    return []
  }
  const paths = new Set<string>()
  for (const block of tool.rawOutput.content) {
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
