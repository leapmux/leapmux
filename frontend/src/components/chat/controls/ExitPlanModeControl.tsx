import type { Component } from 'solid-js'
import type { PlanChoice } from '../model/controlPrompt'
import type { ControlDecisionAction } from './ControlDecisionFooter'
import type { ActionsProps } from './types'

import { approvePlanDecision, planChoiceDecision } from './controlDecision'
import { ControlDecisionFooter } from './ControlDecisionFooter'
import { createPlanApprovalState, planApprovalSwitches } from './planApproval'

export const ExitPlanModeActions: Component<ActionsProps & {
  /**
   * The answers the runtime offers beside Approve and Reject, drawn in the overflow
   * menu. Omit them for a runtime that offers none.
   */
  choices?: readonly PlanChoice[] | undefined
}> = (props) => {
  const planApproval = createPlanApprovalState(props)
  const approval = () => ({
    permissionMode: planApproval.permissionMode() ?? '',
    clearContext: planApproval.clearContext(),
  })

  const handleReject = () => {
    // The composer sends the refusal, with the editor text as what to change.
    props.onTriggerSend()
  }

  const handleApprove = () => props.onDecide(approvePlanDecision(approval()))

  // An approving choice carries the same plan settings as Approve, because it approves
  // the same plan. Any other choice carries none: the Worker refuses plan settings on
  // anything but an approval.
  const choiceActions = (): ControlDecisionAction[] => (props.choices ?? []).map((choice, index) => ({
    label: choice.label,
    testId: `plan-choice-${index}`,
    destructive: choice.effect === 'refuse',
    ...(choice.description
      ? { description: choice.description }
      : {}),
    onSelect: () => props.onDecide(planChoiceDecision(choice, approval())),
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
