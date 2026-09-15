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
import { statusResultCollapsible } from './statusResult'

/**
 * Whether the row holds more than it shows while collapsed.
 *
 * An EXHAUSTIVE switch, not an if-chain with a catch-all. Each body type states
 * its own answer, so a new body type is a compile error here rather than a row
 * that silently reports the plain-text answer for a structured body.
 */
export function toolOutputCollapsible(presentation: ToolPresentation): boolean {
  if (presentation.additionalContent && mcpToolCallCollapsible(presentation.additionalContent))
    return true
  const body = presentation.body
  const outputCollapsible = () => hasMoreLinesThan(presentation.output, COLLAPSED_RESULT_ROWS)
  switch (body.type) {
    case 'agent':
      return hasMoreLinesThan(body.source.body, COLLAPSED_RESULT_ROWS)
    case 'mcp':
      return mcpToolCallCollapsible(body.source)
    case 'diff':
    case 'todo':
    case 'markdown':
      return false
    case 'command':
      return commandOutputIsCollapsible(body.source.output)
    case 'commands':
      return body.entries.some(entry => commandOutputIsCollapsible(entry.source.output))
    case 'search':
      return searchResultCollapsible(body.source)
    case 'status':
      return statusResultCollapsible(body.source)
    case 'directory':
      return directoryResultCollapsible(body.source)
    // A read with no line list drew nothing of its own, so the row shows the
    // plain output and the plain rule applies.
    case 'read':
      return body.source.lines ? body.source.lines.length > COLLAPSED_RESULT_ROWS : outputCollapsible()
    case 'fetch':
    case 'text':
      return outputCollapsible()
    default: {
      const exhaustive: never = body
      void exhaustive
      return outputCollapsible()
    }
  }
}

/**
 * The text the Copy button writes for one body, before the requested changes and
 * the accompanying MCP content join it.
 *
 * Exhaustive for the same reason as {@link toolOutputCollapsible}: the catch-all
 * it replaces already served `fetch` by accident, and it would serve the next
 * body type the same way.
 */
function toolBodyCopyableText(presentation: ToolPresentation): string {
  const body = presentation.body
  switch (body.type) {
    case 'command':
      return body.source.output
    case 'mcp':
      return mcpToolCallCopyable(body.source)
    case 'agent':
      return body.source.body
    // The note the status header holds. The presentation's own output is the raw
    // result text, which that header replaced with the words the reader sees.
    case 'status':
      return body.source.output
    case 'todo':
      return [todosToMarkdown(body.items), body.description].filter(Boolean).join('\n\n')
    case 'markdown':
      return body.text
    // A diff copies its own sources; see `toolPresentationMeta`. Every other
    // body renders from the presentation's plain output, and so copies it.
    case 'commands':
    case 'diff':
    case 'directory':
    case 'fetch':
    case 'read':
    case 'search':
    case 'text':
      return presentation.output
    default: {
      const exhaustive: never = body
      void exhaustive
      return presentation.output
    }
  }
}

export function toolPresentationMeta(presentation: ToolPresentation): ToolResultMeta {
  const body = presentation.body
  const additional = presentation.additionalContent ? mcpToolCallCopyable(presentation.additionalContent) : ''
  // ONE getter behind both fields. `hasCopyable` promised that `copyableContent()`
  // returns a string, and a diff answered it unconditionally -- yet a no-op edit
  // and a streaming diff row both copy to nothing, so the button flashed no
  // "Copied" and wrote no clipboard entry.
  const copyable = (): string | null => [
    body.type === 'diff'
      ? body.sources.map(fileEditCopyableText).filter(Boolean).join('\n\n') || null
      : [toolBodyCopyableText(presentation), requestedFileChangesCopyable(presentation.requestedChanges ?? [])].filter(Boolean).join('\n\n'),
    additional,
  ].filter(Boolean).join('\n\n') || null
  return {
    collapsible: toolOutputCollapsible(presentation),
    hasDiff: (body.type === 'diff' ? body.sources : presentation.requestedChanges ?? []).some(fileEditHasDiff),
    hasCopyable: copyable() !== null,
    copyableContent: copyable,
  }
}
