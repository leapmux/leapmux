import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import { pickObject, pickString } from '~/lib/jsonPick'
import { MarkdownPlanLayout } from '../../../widgets/MarkdownPlanLayout'

/** Render ExitPlanMode tool_use with the plan from input.plan as a markdown document. */
export function renderExitPlanMode(toolUse: Record<string, unknown>, context?: RenderContext): JSX.Element {
  const planText = pickString(pickObject(toolUse, 'input'), 'plan')
  return <MarkdownPlanLayout toolName="ExitPlanMode" title="Leaving Plan Mode" planText={planText} context={context} />
}
