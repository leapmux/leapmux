import type { MessageCategory } from '../../messageClassification'
import type { ToolMessageInput, ToolResultMeta } from '../registry'
import type { ACPToolAdapter } from './toolPresentation'
import type { ToolPresentation } from '~/components/chat/results/toolPresentation'
import { todosToMarkdown } from '~/lib/messageParser'
import { COLLAPSED_RESULT_ROWS, hasMoreLinesThan } from '../../results/collapse'
import { commandOutputIsCollapsible } from '../../results/commandResult'
import { directoryResultCollapsible } from '../../results/directoryResult'
import { fileEditCopyableText, fileEditHasDiff } from '../../results/fileEditDiff'
import { mcpToolCallCollapsible, mcpToolCallCopyable } from '../../results/mcpToolCall'
import { searchResultCollapsible } from '../../results/searchResult'
import { acpToolFinished, acpToolPresentation, parsedACPToolCall, resolveACPToolCall } from './toolPresentation'

export function acpToolOutputCollapsible(presentation: ToolPresentation): boolean {
  const body = presentation.body
  if (body.type === 'agent')
    return hasMoreLinesThan(body.source.body, COLLAPSED_RESULT_ROWS)
  if (body.type === 'mcp')
    return mcpToolCallCollapsible(body.source)
  if (body.type === 'diff' || body.type === 'todo' || body.type === 'markdown')
    return false
  if (body.type === 'command')
    return commandOutputIsCollapsible(body.source.output)
  if (body.type === 'commands')
    return body.entries.some(entry => commandOutputIsCollapsible(entry.source.output))
  if (body.type === 'search')
    return searchResultCollapsible(body.source)
  if (body.type === 'directory')
    return directoryResultCollapsible(body.source)
  if (body.type === 'read' && body.source.lines)
    return body.source.lines.length > COLLAPSED_RESULT_ROWS
  return hasMoreLinesThan(presentation.output, COLLAPSED_RESULT_ROWS)
}

export function acpToolResultMeta(
  category: MessageCategory,
  input: ToolMessageInput,
  adapter?: ACPToolAdapter,
): ToolResultMeta | null {
  if (category.kind !== 'tool_use')
    return null
  const tool = parsedACPToolCall(input.parsed.parentObject)
  if (!tool || !acpToolFinished(tool, input.parsed.completion))
    return null
  const presentation = acpToolPresentation(resolveACPToolCall(tool, input.request?.parentObject), adapter, input.parsed.supplementalContent, input.parsed.completion)
  const body = presentation.body
  const text = body.type === 'command'
    ? body.source.output
    : body.type === 'commands'
      ? presentation.output
      : body.type === 'mcp'
        ? mcpToolCallCopyable(body.source)
        : body.type === 'agent'
          ? body.source.body
          : body.type === 'todo'
            ? todosToMarkdown(body.items)
            : body.type === 'markdown' ? body.text : presentation.output
  return {
    collapsible: acpToolOutputCollapsible(presentation),
    hasDiff: body.type === 'diff' && body.sources.some(fileEditHasDiff),
    hasCopyable: body.type === 'diff' || text.length > 0,
    copyableContent: () => body.type === 'diff'
      ? body.sources.map(fileEditCopyableText).filter(Boolean).join('\n\n') || null
      : text || null,
  }
}
