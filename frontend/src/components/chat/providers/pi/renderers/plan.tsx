import type { RenderContext } from '../../../messageRenderers'
import { Show } from 'solid-js'
import { PI_TOOL } from '~/generated/contracts/pi-protocol'
import { pickString } from '~/lib/jsonPick'
import { MarkdownPlanLayout } from '../../../widgets/MarkdownPlanLayout'
import { piExtractTool, piPairedRequest } from '../extractors/toolCommon'

interface PlanProps {
  payload: Record<string, unknown>
  context?: RenderContext
}

export function PiPlanRequest(props: PlanProps) {
  return <MarkdownPlanLayout toolName={PI_TOOL.PlanComplete} title="Proposed Plan" planText={pickString(piExtractTool(props.payload)?.args, 'plan')} context={props.context} />
}

/** The result repeats the full plan only when the request does not contain it. */
export function PiPlanResult(props: PlanProps) {
  const result = () => piExtractTool(props.payload)?.result
  const plan = () => pickString(result()?.details, 'plan')
  const requestPlan = () => {
    const request = piPairedRequest(props.payload, props.context?.sources?.request())
    return pickString(piExtractTool(request?.parentObject)?.args, 'plan')
  }
  return (
    <Show when={plan().trim()} fallback={<div>{result()?.text}</div>}>
      <Show when={!requestPlan().trim()} fallback={<div>Plan ready for review.</div>}>
        <MarkdownPlanLayout toolName={PI_TOOL.PlanComplete} title="Proposed Plan" planText={plan()} context={props.context} />
      </Show>
    </Show>
  )
}
