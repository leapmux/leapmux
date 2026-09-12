import type { Component } from 'solid-js'
import type { ActionsProps } from './types'

import { buildAllowResponse, getToolInput } from '~/utils/controlResponse'
import { ControlDecisionFooter } from './ControlDecisionFooter'
import { createPlanApprovalState, planApprovalSwitches } from './planApproval'
import { sendResponse } from './types'

export const ExitPlanModeActions: Component<ActionsProps> = (props) => {
  const planApproval = createPlanApprovalState(props)

  const handleReject = () => {
    // Editor text is used as reject comment via onSend handler
    props.onTriggerSend()
  }

  const handleApprove = () => sendResponse(props.onRespond, buildAllowResponse(props.request.requestId, getToolInput(props.request.payload), {
    permissionMode: planApproval.permissionMode(),
    clearContext: planApproval.clearContext(),
  }))

  return (
    <ControlDecisionFooter
      hasEditorContent={props.hasEditorContent}
      onSendFeedback={handleReject}
      negativeAction={{ label: 'Reject', testId: 'plan-reject-btn', onSelect: handleReject }}
      positiveAction={{ label: 'Approve', testId: 'plan-approve-btn', onSelect: handleApprove }}
      switches={() => planApprovalSwitches(planApproval)}
      permissionPill={planApproval.permissionPill}
    />
  )
}
