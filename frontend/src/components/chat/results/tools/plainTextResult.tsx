import type { JSX } from 'solid-js'
import type { ToolRowView } from './renderer'
import { CollapsibleContent } from '../CollapsibleContent'
import { useCollapsedLines } from '../useCollapsedLines'

/**
 * The one fallback body: a plain, collapsible text block.
 *
 * It draws a `FailedResult` and an `UnparsedResult`, so it is owned by the row
 * component and never by a kind -- a kind whose result arrived unreadable says
 * so in words the row spells, not in a body the kind pretends to understand.
 */
export function PlainTextResult(props: { text: string, view: ToolRowView }): JSX.Element {
  const collapsed = useCollapsedLines({ text: () => props.text, expanded: () => props.view.expanded() })
  return <CollapsibleContent kind="ansi-or-pre" text={props.text} display={collapsed.display()} isCollapsed={collapsed.isCollapsed()} {...(props.view.context !== undefined ? { context: props.view.context } : {})} />
}
