import type { ToolResultMeta } from '../providers/registry'
import type { ToolPresentation } from '~/components/chat/results/toolPresentation'
import { todosToMarkdown } from '~/lib/messageParser'
import { COLLAPSED_RESULT_ROWS, hasMoreLinesThan } from './collapse'
import { commandOutputIsCollapsible } from './commandResult'
import { directoryResultCollapsible } from './directoryResult'
import { fileEditCopyableText, fileEditHasDiff } from './fileEditDiff'
import { mcpToolCallCollapsible, mcpToolCallCopyable } from './mcpToolCall'
import { requestedFileChangesCopyable } from './requestedFileChanges'
import { searchResultCollapsible } from './searchResult'

export function toolOutputCollapsible(presentation: ToolPresentation): boolean {
  if (presentation.additionalContent && mcpToolCallCollapsible(presentation.additionalContent))
    return true
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

export function toolPresentationMeta(presentation: ToolPresentation): ToolResultMeta {
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
  const additional = presentation.additionalContent ? mcpToolCallCopyable(presentation.additionalContent) : ''
  return {
    collapsible: toolOutputCollapsible(presentation),
    hasDiff: (body.type === 'diff' ? body.sources : presentation.requestedChanges ?? []).some(fileEditHasDiff),
    hasCopyable: body.type === 'diff' || text.length > 0 || additional.length > 0 || (presentation.requestedChanges?.length ?? 0) > 0,
    copyableContent: () => [body.type === 'diff'
      ? body.sources.map(fileEditCopyableText).filter(Boolean).join('\n\n') || null
      : [text, requestedFileChangesCopyable(presentation.requestedChanges ?? [])].filter(Boolean).join('\n\n'), additional].filter(Boolean).join('\n\n') || null,
  }
}
