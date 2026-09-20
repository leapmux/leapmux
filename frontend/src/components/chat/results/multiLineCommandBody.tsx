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

/** Collapsed command summary: full text, clipped to three visual rows. */
export function CommandInputSummary(props: {
  command: string
  language?: CommandLanguage
  context?: ToolResultRenderContext
  collapsed?: boolean
  onOverflowChange?: (overflowing: boolean) => void
}): JSX.Element {
  let element: HTMLDivElement | undefined
  const [overflowing, setOverflowing] = createSignal(false)
  const displayCommand = createMemo(() => props.collapsed ? collapsedCommandSummaryText(props.command) : props.command)

  // The functional setter form reads the previous value WITHOUT tracking it, so a
  // measurement that runs inside a reactive scope -- `elementRef` does -- cannot make
  // that scope depend on the signal it writes.
  const setOverflowingState = (next: boolean): void => {
    setOverflowing((prev) => {
      if (prev !== next)
        props.onOverflowChange?.(next)
      return next
    })
  }

  /**
   * Measure the COLLAPSED summary, and only the collapsed one.
   *
   * An expanded summary has no clip to measure, and reporting `false` for it destroyed
   * the fact that made the row expandable in the first place. `ToolMessage` keeps this
   * answer in `summaryOverflows` and reads it back as `expandable`, so the chevron
   * disappeared the moment a reader used it: the row stayed at full height with no way
   * to collapse it again. The last collapsed answer is the one the header needs, so an
   * expanded row leaves it alone.
   */
  const measureOverflow = (): void => {
    if (!element || !props.collapsed)
      return
    setOverflowingState(commandInputOverflowsCollapsedRows(element))
  }

  createEffect(() => {
    // Track content and collapsed state; tokenization may swap text nodes for spans,
    // but the row height should stay stable. The frame read catches the post-render
    // layout and avoids applying the fade to summaries that fit in three rows.
    const command = displayCommand()
    if (!props.collapsed)
      return command
    const cancel = scheduleOverflowMeasure(measureOverflow)
    onCleanup(cancel)
    return command
  })

  createEffect(() => {
    if (!element || !props.collapsed || typeof ResizeObserver === 'undefined')
      return
    const observer = createRafResizeObserver(() => measureOverflow())
    observer?.observe(element)
    onCleanup(() => observer?.disconnect())
  })

  return (
    <CommandHighlightHtml
      {...(props.language !== undefined ? { language: props.language } : {})}
      class={joinClasses(toolInputSummary, props.collapsed && commandInputCollapsed, props.collapsed && overflowing() && commandInputCollapsedFade)}
      code={displayCommand()}
      {...(props.context !== undefined ? { context: props.context } : {})}
      {...(props.collapsed !== undefined
        ? { dataCommandInputCollapsed: props.collapsed, dataCommandInputOverflowing: props.collapsed && overflowing() }
        : {})}
      elementRef={(el) => {
        element = el
        measureOverflow()
      }}
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
