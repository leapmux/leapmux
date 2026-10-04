import type { JSX } from 'solid-js'
import type { ToolRowView } from './renderer'
import { CollapsibleContent } from '../CollapsibleContent'
import { useCollapsedLines } from '../useCollapsedLines'

/**
 * Render failure prose and unreadable result text as one collapsible plain-text body.
 * The row owns this fallback because no typed kind can interpret that result.
 */
export function PlainTextResult(props: { text: string, view: ToolRowView }): JSX.Element {
  const collapsed = useCollapsedLines({ text: () => props.text, expanded: () => props.view.expanded() })
  return <CollapsibleContent outputPreview kind="ansi-or-pre" text={props.text} display={collapsed.display()} isCollapsed={collapsed.isCollapsed()} {...(props.view.context !== undefined ? { context: props.view.context } : {})} />
}
