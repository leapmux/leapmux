import type { Component } from 'solid-js'
import type { ActionsProps } from '../../controls/types'
import { buildAllowResponse, buildDenyResponse } from '~/utils/controlResponse'
import { ControlDecisionFooter } from '../../controls/ControlDecisionFooter'
import { sendResponse } from '../../controls/types'

export const CursorControlActions: Component<ActionsProps> = (props) => {
  const createPlanAllow = () => sendResponse(props.onRespond, buildAllowResponse(props.request.requestId, {}))

  const createPlanReject = () => sendResponse(props.onRespond,
    // Bare deny (no typed reason): buildDenyResponse fills the shared
    // CONTROL_REJECTED_BY_USER_MESSAGE placeholder, so don't re-spell the literal here.
    buildDenyResponse(props.request.requestId))

  return (
    <ControlDecisionFooter
      hasEditorContent={props.hasEditorContent}
      onSendFeedback={props.onTriggerSend}
      negativeAction={{ label: 'Reject', testId: 'control-deny-btn', onSelect: createPlanReject }}
      positiveAction={{ label: 'Approve', testId: 'control-allow-btn', onSelect: createPlanAllow }}
    />
  )
}
