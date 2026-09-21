import type { JSX } from 'solid-js'
import type { NumberedFileLine, ReadFileResult, ReminderSeverity } from '../model/readFileResult'
import type { ToolResultRenderContext } from '../renderContext'
import type { AlertVariant } from '~/components/common/Alert'
import { createMemo, For, Show } from 'solid-js'
import { Alert } from '~/components/common/Alert'
import { getToolResultExpanded, shouldPauseSyntaxHighlighting } from '../messageRenderers'
import { toolMessage, toolResultCollapsed } from '../toolStyles.css'
import { CollapsibleContent } from './CollapsibleContent'
import { EMPTY_RESULT_NOTICE } from './emptyResultNotice'
import { ReadResultView } from './ReadResultView'
import { useCollapsedItems, useCollapsedLines } from './useCollapsedLines'

// Stable empty fallback so memo equality holds when `lines` is null --
// otherwise every read re-allocates `[]` and downstream `displayItems`
// trips its equality check on every render.
const EMPTY_LINES: readonly NumberedFileLine[] = []

/**
 * Draw one reminder severity as an alert style.
 *
 * The one place the model's severity vocabulary meets the `Alert` component's.
 * The two spell the same words today, so the map reads as identity -- it earns
 * its place by being the only edit an alert-style rename needs.
 */
const REMINDER_ALERT_VARIANT: Record<ReminderSeverity, AlertVariant> = {
  success: 'success',
  warning: 'warning',
  danger: 'danger',
  error: 'error',
}

function reminderVariant(severity: ReminderSeverity | undefined): AlertVariant | undefined {
  return severity === undefined ? undefined : REMINDER_ALERT_VARIANT[severity]
}

export function ReadFileResultBody(props: {
  source: ReadFileResult
  path?: string
  context?: ToolResultRenderContext
}): JSX.Element {
  const expanded = () => getToolResultExpanded(props.context)
  const items = createMemo<NumberedFileLine[]>(() => props.source.lines ?? (EMPTY_LINES as NumberedFileLine[]))
  const fallbackText = () => props.source.fallbackContent || EMPTY_RESULT_NOTICE
  const fallback = useCollapsedLines({ text: fallbackText, expanded })
  // An empty list draws the fallback the same way an absent one does, so the body
  // states a refused read's reason instead of nothing.
  const hasParsedLines = () => props.source.lines !== null && props.source.lines.length > 0
  const { isCollapsed, displayItems } = useCollapsedItems<NumberedFileLine>({ items, expanded })
  const collapsedClass = () => hasParsedLines() && isCollapsed() ? ` ${toolResultCollapsed}` : ''

  return (
    <div class={`${toolMessage}${collapsedClass()}`}>
      {/* Reminder/tag alerts render only when expanded, so the collapsed default
          stays the body-only height the off-screen estimator assumes. */}
      <Show when={expanded()}>
        <For each={props.source.leading ?? []}>
          {(r) => {
            const variant = reminderVariant(r.severity)
            return <Alert {...(variant !== undefined ? { variant } : {})} label={r.label}>{r.text}</Alert>
          }}
        </For>
      </Show>
      <Show
        when={hasParsedLines() && items().length > 0}
        fallback={<CollapsibleContent kind="pre" text={fallbackText()} display={fallback.display()} isCollapsed={fallback.isCollapsed()} {...(props.context !== undefined ? { context: props.context } : {})} />}
      >
        <ReadResultView
          lines={displayItems()}
          {...(props.path !== undefined ? { filePath: props.path } : {})}
          {...(props.context?.premeasureMode !== undefined ? { premeasureMode: props.context?.premeasureMode } : {})}
          syntaxHighlightingPaused={shouldPauseSyntaxHighlighting(props.context)}
          {...(props.context?.textSelectionActive !== undefined ? { textSelectionActive: props.context?.textSelectionActive } : {})}
        />
      </Show>
      <Show when={expanded()}>
        <For each={props.source.trailing ?? []}>
          {(r) => {
            const variant = reminderVariant(r.severity)
            return <Alert {...(variant !== undefined ? { variant } : {})} label={r.label}>{r.text}</Alert>
          }}
        </For>
      </Show>
    </div>
  )
}
