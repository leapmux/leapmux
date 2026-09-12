import type { Component } from 'solid-js'
import type { ActionsProps } from '../../controls/types'
import { PI_DIALOG_METHOD, PI_EVENT, PI_PLAN_ACTION, PI_PLAN_DIALOG } from '~/generated/contracts/pi-protocol'
import { pickString } from '~/lib/jsonPick'
import { ControlDecisionFooter } from '../../controls/ControlDecisionFooter'
import { createPlanApprovalState, planApprovalSwitches } from '../../controls/planApproval'
import { piSelectOptions } from './askUserQuestion'
import { piValueResponse, sendPiExtensionResponse } from './controlResponse'

const approvalValues: readonly string[] = Object.values(PI_PLAN_ACTION)

/** Keep the selected implementation settings visible when the user approves the plan. */
export function piPlanApprovalDetails(payload: Record<string, unknown>): string[] {
  const lines = pickString(payload, 'title').split('\n').map(line => line.trim())
  return ['Model: ', 'Plan reinjection: '].flatMap((prefix) => {
    const line = lines.find(line => line.startsWith(prefix))
    return line ? [line] : []
  })
}

/** Match the native menu before assigning approval semantics to its string choices. */
export function isPiPlanApproval(payload: Record<string, unknown>): boolean {
  if (payload.type !== PI_EVENT.ExtensionUIRequest || payload.method !== PI_DIALOG_METHOD.Select
    || pickString(payload, 'title').split('\n', 1)[0].trim() !== PI_PLAN_DIALOG.ReadyTitle) {
    return false
  }
  const options = payload.options
  return Array.isArray(options)
    && options.every(option => typeof option === 'string' && option.trim())
    && new Set(options).size === options.length
    && approvalValues.every(option => options.includes(option))
}

export const PiPlanApprovalActions: Component<ActionsProps> = (props) => {
  const state = createPlanApprovalState(props)
  const send = (value: string) => sendPiExtensionResponse(props.onRespond, piValueResponse(props.request.requestId, value))
  const additional = () => piSelectOptions(props.request.payload).map(option => option.label).filter(option => !approvalValues.includes(option))
  return (
    <ControlDecisionFooter
      hasEditorContent={props.hasEditorContent}
      onSendFeedback={props.onTriggerSend}
      negativeAction={{ label: 'Reject', testId: 'plan-reject-btn', onSelect: () => send(PI_PLAN_ACTION.Stay) }}
      positiveAction={{ label: 'Approve', testId: 'plan-approve-btn', onSelect: () => send(state.clearContext() ? PI_PLAN_ACTION.ImplementFresh : PI_PLAN_ACTION.ImplementHere) }}
      switches={() => planApprovalSwitches(state)}
      additionalActions={() => additional().map((option, index) => ({ label: option, testId: `pi-plan-action-${index}`, onSelect: () => send(option) }))}
    />
  )
}
