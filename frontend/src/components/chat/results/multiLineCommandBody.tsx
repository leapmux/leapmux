import type { JSX } from 'solid-js'
import type { CommandLanguage } from '../model/tools/execute'
import type { ToolResultRenderContext } from '../renderContext'
import { createEffect, createMemo, createSignal, onCleanup } from 'solid-js'
import { createRafResizeObserver } from '~/lib/resizeObserver'
import { COMMAND_INPUT_HIGHLIGHT_CHAR_LIMIT } from '../chatHeightShared'
import { CommandHighlightHtml } from '../syntaxHighlight'
import { commandInputCollapsed, commandInputCollapsedFade, toolInputSummary, toolResultContentAnsi } from '../toolStyles.css'

function joinClasses(...classes: Array<string | false | null | undefined>): string {
  return classes.filter(Boolean).join(' ')
}

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

/** Measure a summary only while it uses the shared collapsed-row limit. */
export function useCollapsedSummaryOverflow(props: {
  collapsed: () => boolean
  content: () => unknown
  onOverflowChange?: (overflowing: boolean) => void
}): {
  overflowing: () => boolean
  elementRef: (element: HTMLElement) => void
} {
  let element: HTMLElement | undefined
  const [overflowing, setOverflowing] = createSignal(false)

  // The functional setter reads the previous value without tracking it. A
  // measurement inside a reactive scope does not make that scope depend on the
  // signal that it writes.
  const setOverflowingState = (next: boolean): void => {
    setOverflowing((previous) => {
      if (previous !== next)
        props.onOverflowChange?.(next)
      return next
    })
  }

  /**
   * Measure the collapsed summary only.
   *
   * An expanded summary has no clip to measure. Reporting `false` during expansion
   * removes the fact that made the row expandable, so the control disappears while
   * the row stays expanded. Keep the last collapsed result until the row collapses.
   */
  const measureOverflow = (): void => {
    if (!element || !props.collapsed())
      return
    setOverflowingState(commandInputOverflowsCollapsedRows(element))
  }

  createEffect(() => {
    // Track the content and collapsed state. The frame read measures the completed
    // layout after tokenization or list rendering changes the child nodes.
    props.content()
    if (!props.collapsed())
      return
    const cancel = scheduleOverflowMeasure(measureOverflow)
    onCleanup(cancel)
  })

  createEffect(() => {
    if (!element || !props.collapsed() || typeof ResizeObserver === 'undefined')
      return
    const observer = createRafResizeObserver(() => measureOverflow())
    observer?.observe(element)
    onCleanup(() => observer?.disconnect())
  })

  return {
    overflowing,
    elementRef: (next) => {
      element = next
      measureOverflow()
    },
  }
}

/** Collapsed command summary: full text, clipped to three visual rows. */
export function CommandInputSummary(props: {
  command: string
  language?: CommandLanguage
  context?: ToolResultRenderContext
  collapsed?: boolean
  onOverflowChange?: (overflowing: boolean) => void
}): JSX.Element {
  const displayCommand = createMemo(() => props.collapsed ? collapsedCommandSummaryText(props.command) : props.command)
  const overflow = useCollapsedSummaryOverflow({
    collapsed: () => props.collapsed === true,
    content: displayCommand,
    onOverflowChange: value => props.onOverflowChange?.(value),
  })

  return (
    <CommandHighlightHtml
      {...(props.language !== undefined ? { language: props.language } : {})}
      class={joinClasses(toolInputSummary, props.collapsed && commandInputCollapsed, props.collapsed && overflow.overflowing() && commandInputCollapsedFade)}
      code={displayCommand()}
      {...(props.context !== undefined ? { context: props.context } : {})}
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
      {...(props.language !== undefined ? { language: props.language } : {})}
      class={toolResultContentAnsi}
      code={props.command}
      {...(props.context !== undefined ? { context: props.context } : {})}
      maxHighlightChars={COMMAND_INPUT_HIGHLIGHT_CHAR_LIMIT}
    />
  )
}
