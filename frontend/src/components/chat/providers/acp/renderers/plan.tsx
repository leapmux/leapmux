import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import { TodoListMessage } from '../../../todoListMessage'
import { acpPlanFromEntries } from '../extractors/plan'

/** Render an ACP plan (todo list). */
export function acpPlanRenderer(toolUse: Record<string, unknown>, context?: RenderContext): JSX.Element | null {
  const entries = toolUse.entries as Array<{ priority?: string, status?: string, content: string }> | undefined
  const source = acpPlanFromEntries(entries)
  if (!source)
    return null
  return <TodoListMessage source={source} context={context} />
}
