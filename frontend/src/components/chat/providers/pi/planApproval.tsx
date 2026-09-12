import type { Component } from 'solid-js'
import type { ActionsProps } from '../../controls/types'
import { PI_PLAN_ACTION } from '~/generated/contracts/pi-protocol'
import { ControlDecisionFooter } from '../../controls/ControlDecisionFooter'
import { createPlanApprovalState, planApprovalSwitches } from '../../controls/planApproval'
import { piSelectOptions } from './askUserQuestion'
import { piValueResponse, sendPiExtensionResponse } from './controlResponse'

const approvalValues: readonly string[] = Object.values(PI_PLAN_ACTION)

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
