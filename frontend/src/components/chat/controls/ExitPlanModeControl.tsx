import type { Component } from 'solid-js'
import type { PlanChoice } from '../model/controlPrompt'
import type { ControlDecisionAction } from './ControlDecisionFooter'
import type { ActionsProps } from './types'

import { buildAllowResponse, buildDenyResponse, getToolInput, withControlChoice } from '~/utils/controlResponse'
import { ControlDecisionFooter } from './ControlDecisionFooter'
import { createPlanApprovalState, planApprovalSwitches } from './planApproval'
import { sendResponse } from './types'

export const ExitPlanModeActions: Component<ActionsProps & {
  /**
   * The answers the runtime offers beside Approve and Reject, drawn in the overflow
   * menu. Omit them for a runtime that offers none.
   */
  choices?: readonly PlanChoice[] | undefined
}> = (props) => {
  const planApproval = createPlanApprovalState(props)
  const planApprovalSettings = () => ({ planApproval: { permissionMode: planApproval.permissionMode() ?? '', clearContext: planApproval.clearContext() } })

  const handleReject = () => {
    // The send handler uses the editor text as rejection feedback.
    props.onTriggerSend()
  }

  const handleApprove = () => sendResponse(
    props.onRespond,
    buildAllowResponse(props.request.requestId, getToolInput(props.request.payload)),
    planApprovalSettings(),
  )

  // An approving choice carries the same plan settings as Approve, because it approves
  // the same plan. A refusing one carries none: the service refuses plan settings on
  // anything but an approval.
  const choiceActions = (): ControlDecisionAction[] => (props.choices ?? []).map((choice, index) => ({
    label: choice.label,
    testId: `plan-choice-${index}`,
    destructive: !choice.approves,
    ...(choice.description ? { description: choice.description } : {}),
    onSelect: () => choice.approves
      ? sendResponse(
          props.onRespond,
          withControlChoice(buildAllowResponse(props.request.requestId, getToolInput(props.request.payload)), choice.id),
          planApprovalSettings(),
        )
      : sendResponse(props.onRespond, withControlChoice(buildDenyResponse(props.request.requestId), choice.id)),
  }))

  return (
    <ControlDecisionFooter
      hasEditorContent={props.hasEditorContent}
      onSendFeedback={handleReject}
      negativeAction={{ label: 'Reject', testId: 'plan-reject-btn', onSelect: handleReject }}
      positiveAction={{ label: 'Approve', testId: 'plan-approve-btn', onSelect: handleApprove }}
      switches={() => planApprovalSwitches(planApproval)}
      permissionPill={planApproval.permissionPill}
      additionalActions={choiceActions}
    />
  )
}
