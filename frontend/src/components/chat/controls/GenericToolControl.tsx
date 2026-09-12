import type { Component } from 'solid-js'
import type { ActionsProps } from './types'
import type { ControlRequest } from '~/stores/control.store'

import { buildAllowResponse, buildDenyResponse, getToolInput, getToolName } from '~/utils/controlResponse'
import * as styles from '../ControlRequestBanner.css'
import { ControlDecisionFooter } from './ControlDecisionFooter'
import { ControlJson } from './ControlJson'
import { buildSessionPermissionPill, createSessionPermissionPresetChoice, respondThenApplyPermissionPreset } from './permissionPresets'
import { sendResponse } from './types'

export const GenericToolContent: Component<{ request: ControlRequest }> = (props) => {
  const toolName = () => getToolName(props.request.payload)
  const input = () => getToolInput(props.request.payload)

  return (
    <>
      <div class={styles.controlBannerTitle}>
        Permission Required:
        {toolName()}
      </div>
      <ControlJson value={input()} />
    </>
  )
}

export const GenericToolActions: Component<ActionsProps> = (props) => {
  const permissionChoice = createSessionPermissionPresetChoice(props)

  const handleDeny = () => {
    return sendResponse(props.onRespond, buildDenyResponse(props.request.requestId))
  }

  // Send the approval before applying a preset, because some permission changes restart the agent.
  // Concurrent requests could restart the agent before it receives the approval.
  const handleAllow = () => respondThenApplyPermissionPreset(
    sendResponse(props.onRespond, buildAllowResponse(props.request.requestId, getToolInput(props.request.payload))),
    props.presets,
    permissionChoice.choice(),
  )

  return (
    <ControlDecisionFooter
      hasEditorContent={props.hasEditorContent}
      onSendFeedback={props.onTriggerSend}
      negativeAction={{ label: 'Deny', testId: 'control-deny-btn', onSelect: handleDeny }}
      positiveAction={{ label: 'Allow', testId: 'control-allow-btn', onSelect: handleAllow }}
      permissionPill={() => buildSessionPermissionPill(props.presets, permissionChoice)}
    />
  )
}
