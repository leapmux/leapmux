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
  const build = (): string | null => [
    body.type === 'diff'
      ? body.sources.map(fileEditCopyableText).filter(Boolean).join('\n\n') || null
      : [toolBodyCopyableText(presentation), requestedFileChangesCopyable(presentation.requestedChanges ?? [])].filter(Boolean).join('\n\n'),
    additional,
  ].filter(Boolean).join('\n\n') || null
  // ONE getter behind both fields, and it runs AT MOST ONCE. `hasCopyable`
  // promised that `copyableContent()` returns a string, and a diff answered it
  // unconditionally -- yet a no-op edit and a streaming diff row both copy to
  // nothing, so the button flashed no "Copied" and wrote no clipboard entry.
  // Answering that promise truthfully means building the text, and the cache is
  // what keeps the price at one build: `fileEditCopyableText` runs `diffLines`
  // over every changed file, this function runs inside a memo that recomputes on
  // every streamed token, and the Copy button calls the getter again.
  let built: string | null | undefined
  const copyable = (): string | null => (built === undefined ? (built = build()) : built)
  return {
    collapsible: toolOutputCollapsible(presentation),
    hasDiff: (body.type === 'diff' ? body.sources : presentation.requestedChanges ?? []).some(fileEditHasDiff),
    hasCopyable: copyable() !== null,
    copyableContent: copyable,
  }
}

/**
 * Whether the row repeats its own input as a raw JSON summary, because nothing
 * else states what the call asked for.
 *
 * EXHAUSTIVE, for the reason {@link toolOutputCollapsible} gives. The three
 * bodies that answer false draw the input themselves: an MCP body prints its
 * arguments, a todo body prints the list, and a markdown body IS the input.
 */
export function toolBodyRepeatsInput(body: ToolPresentation['body']): boolean {
  switch (body.type) {
    case 'mcp':
    case 'todo':
    case 'markdown':
      return false
    case 'agent':
    case 'command':
    case 'commands':
    case 'diff':
    case 'directory':
    case 'fetch':
    case 'read':
    case 'search':
    case 'status':
    case 'text':
      return true
    default: {
      const exhaustive: never = body
      void exhaustive
      return true
    }
  }
}

/**
 * Whether the body draws its own failure or cancellation notice, so the row must
 * not draw the shared outcome header above it as well.
 *
 * EXHAUSTIVE, for the reason {@link toolOutputCollapsible} gives.
 */
export function toolBodyStatesOwnOutcome(body: ToolPresentation['body']): boolean {
  switch (body.type) {
    case 'agent':
    case 'command':
    case 'commands':
    case 'status':
      return true
    case 'diff':
    case 'directory':
    case 'fetch':
    case 'markdown':
    case 'mcp':
    case 'read':
    case 'search':
    case 'text':
    case 'todo':
      return false
    default: {
      const exhaustive: never = body
      void exhaustive
      return false
    }
  }
}
