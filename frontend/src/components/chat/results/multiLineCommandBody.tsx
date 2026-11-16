import type { JSX } from 'solid-js'
import type { CommandLanguage } from '../model/tools/execute'
import type { ToolResultRenderContext } from '../renderContext'
import { createMemo, onCleanup } from 'solid-js'
import { joinClassNames } from '~/lib/classNames'
import { COMMAND_INPUT_HIGHLIGHT_CHAR_LIMIT } from '../chatHeightShared'
import { CommandHighlightHtml } from '../syntaxHighlight'
import { commandInputCollapsed, commandInputCollapsedFade, toolInputSummary, toolResultContentAnsi } from '../toolStyles.css'
import { useCollapsedOverflow } from '../widgets/collapsedOverflow'

function commandInputOverflowsCollapsedRows(el: HTMLElement): boolean {
  return el.scrollHeight > el.clientHeight + 1
}

function collapsedCommandSummaryText(command: string): string {
  return command.replace(/^(?:[ \t]*\r?\n)+/, '')
}

function scheduleOverflowMeasure(measure: () => void): () => void {
  if (typeof requestAnimationFrame === 'function') {
    const frame = requestAnimationFrame(measure)
    return () => cancelAnimationFrame(frame)
  }
  const timeout = setTimeout(measure, 0)
  return () => clearTimeout(timeout)
}

/**
 * Measure a summary only while it uses the shared collapsed-row limit (see
 * useCollapsedOverflow for the rules that it shares with the header title).
 *
 * It measures again when the box resizes, and one frame after its content or its forced
 * answer changes: the frame read measures the completed layout after tokenization or
 * list rendering changes the child nodes.
 */
export function useCollapsedSummaryOverflow(props: {
  collapsed: () => boolean
  content: () => unknown
  forcedOverflow?: () => boolean
  onOverflowChange?: (overflowing: boolean) => void
}): {
  overflowing: () => boolean
  elementRef: (element: HTMLElement) => void
} {
  const overflow = useCollapsedOverflow({
    collapsed: () => props.collapsed(),
    clips: element => (props.forcedOverflow?.() ?? false) || commandInputOverflowsCollapsedRows(element),
    watch: (_element, measure) => {
      props.content()
      props.forcedOverflow?.()
      onCleanup(scheduleOverflowMeasure(measure))
    },
    onOverflowChange: next => props.onOverflowChange?.(next),
  })
  return { overflowing: overflow.overflowing, elementRef: overflow.ref }
}

/** Collapsed command summary: full text, clipped to three visual rows. */
export function CommandInputSummary(props: {
  command: string
  language?: CommandLanguage
  context?: ToolResultRenderContext
  collapsed?: boolean
  onOverflowChange?: (overflowing: boolean) => void
}): JSX.Element {
  const displayCommand = createMemo(() => props.collapsed
    ? collapsedCommandSummaryText(props.command)
    : props.command)
  const overflow = useCollapsedSummaryOverflow({
    collapsed: () => props.collapsed === true,
    content: displayCommand,
    onOverflowChange: value => props.onOverflowChange?.(value),
  })

  return (
    <CommandHighlightHtml
      {...(props.language !== undefined
        ? { language: props.language }
        : {})}
      class={joinClassNames(toolInputSummary, props.collapsed && commandInputCollapsed, props.collapsed && overflow.overflowing() && commandInputCollapsedFade)}
      code={displayCommand()}
      {...(props.context !== undefined
        ? { context: props.context }
        : {})}
      {...(props.collapsed !== undefined
        ? { dataCommandInputCollapsed: props.collapsed, dataCommandInputOverflowing: props.collapsed && overflow.overflowing() }
        : {})}
      elementRef={overflow.elementRef}
      maxHighlightChars={COMMAND_INPUT_HIGHLIGHT_CHAR_LIMIT}
    />
  )
}

/** Full command body shown after expanding a command input summary. */
export function CommandInputBody(props: { command: string, language?: CommandLanguage, context?: ToolResultRenderContext }): JSX.Element {
  return (
    <CommandHighlightHtml
      {...(props.language !== undefined
        ? { language: props.language }
        : {})}
      class={toolResultContentAnsi}
      code={props.command}
      {...(props.context !== undefined
        ? { context: props.context }
        : {})}
      maxHighlightChars={COMMAND_INPUT_HIGHLIGHT_CHAR_LIMIT}
    />
  )
}
