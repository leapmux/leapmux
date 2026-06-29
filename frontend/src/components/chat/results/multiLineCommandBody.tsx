import type { Accessor, JSX } from 'solid-js'
import type { RenderContext } from '../messageRenderers'
import { createEffect, createMemo, createSignal, on, onCleanup } from 'solid-js'
import { createRafResizeObserver } from '~/lib/resizeObserver'
import { COMMAND_INPUT_HIGHLIGHT_CHAR_LIMIT, commandInputNeedsExpansion as commandInputNeedsExpansionShared, isMultiLineCommand as isMultiLineCommandShared } from '../chatHeightShared'
import { BashHighlightHtml } from '../toolRenderers'
import { commandInputCollapsed, commandInputCollapsedFade, toolInputSummary, toolResultContentAnsi } from '../toolStyles.css'

function joinClasses(...classes: Array<string | false | null | undefined>): string {
  return classes.filter(Boolean).join(' ')
}

/** True when the command spans more than one hard line. */
export function isMultiLineCommand(command: string | null | undefined): boolean {
  return isMultiLineCommandShared(command)
}

/** True when a command should expose the full-command expansion affordance. */
export function commandInputNeedsExpansion(command: string | null | undefined): boolean {
  return commandInputNeedsExpansionShared(command)
}

export function createCommandInputExpansionState(command: Accessor<string | null | undefined>): {
  commandExpandable: Accessor<boolean>
  setSummaryOverflows: (overflowing: boolean) => void
} {
  const [summaryOverflows, setSummaryOverflows] = createSignal(false)
  createEffect(on(command, () => setSummaryOverflows(false), { defer: true }))
  const commandExpandable = createMemo(() => {
    const value = command()
    return !!value && (commandInputNeedsExpansion(value) || summaryOverflows())
  })
  return { commandExpandable, setSummaryOverflows }
}

function commandInputOverflowsCollapsedRows(el: HTMLElement): boolean {
  return el.scrollHeight > el.clientHeight + 1
}

export function collapsedCommandSummaryText(command: string): string {
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
  context?: RenderContext
  collapsed?: boolean
  onOverflowChange?: (overflowing: boolean) => void
}): JSX.Element {
  let element: HTMLDivElement | undefined
  const [overflowing, setOverflowing] = createSignal(false)
  const displayCommand = createMemo(() => props.collapsed ? collapsedCommandSummaryText(props.command) : props.command)

  const setOverflowingState = (next: boolean): void => {
    setOverflowing((prev) => {
      if (prev !== next)
        props.onOverflowChange?.(next)
      return next
    })
  }

  const measureOverflow = (): void => {
    if (!element || !props.collapsed) {
      setOverflowingState(false)
      return
    }
    setOverflowingState(commandInputOverflowsCollapsedRows(element))
  }

  createEffect(() => {
    // Track content and collapsed state; tokenization may swap text nodes for spans,
    // but the row height should stay stable. The frame read catches the post-render
    // layout and avoids applying the fade to summaries that fit in three rows.
    const command = displayCommand()
    if (!props.collapsed) {
      setOverflowingState(false)
      return command
    }
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
    <BashHighlightHtml
      class={joinClasses(toolInputSummary, props.collapsed && commandInputCollapsed, props.collapsed && overflowing() && commandInputCollapsedFade)}
      code={displayCommand()}
      context={props.context}
      dataCommandInputCollapsed={props.collapsed}
      dataCommandInputOverflowing={props.collapsed && overflowing()}
      elementRef={(el) => {
        element = el
        measureOverflow()
      }}
      maxHighlightChars={COMMAND_INPUT_HIGHLIGHT_CHAR_LIMIT}
    />
  )
}

/** Full command body shown after expanding a command input summary. */
export function CommandInputBody(props: { command: string, context?: RenderContext }): JSX.Element {
  return (
    <BashHighlightHtml
      class={toolResultContentAnsi}
      code={props.command}
      context={props.context}
      maxHighlightChars={COMMAND_INPUT_HIGHLIGHT_CHAR_LIMIT}
    />
  )
}
