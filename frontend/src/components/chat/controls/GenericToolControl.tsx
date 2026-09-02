import type { Component } from 'solid-js'
import type { ActionsProps } from './types'
import type { ControlRequest } from '~/stores/control.store'

import { createSignal } from 'solid-js'
import { buildAllowResponse, buildDenyResponse, getToolInput, getToolName } from '~/utils/controlResponse'
import * as styles from '../ControlRequestBanner.css'
import { CollapsibleText } from './CollapsibleText'
import { ControlDecisionFooter } from './ControlDecisionFooter'
import { sendResponse } from './types'

export const GenericToolContent: Component<{ request: ControlRequest }> = (props) => {
  const toolName = () => getToolName(props.request.payload)
  const input = () => getToolInput(props.request.payload)
  const inputSummary = () => {
    try {
      return JSON.stringify(input(), null, 2)
    }
    catch {
      return '{}'
    }
  }

  return (
    <>
      <div class={styles.controlBannerTitle}>
        Permission Required:
        {toolName()}
      </div>
      <CollapsibleText text={inputSummary()} maxLines={6} class={styles.bannerCodeBlock} />
    </>
  )
}

export const GenericToolActions: Component<ActionsProps> = (props) => {
  const [bypass, setBypass] = createSignal(false)

  const handleDeny = () => {
    return sendResponse(props.request.agentId, props.onRespond, buildDenyResponse(props.request.requestId))
  }

  // Await the allow BEFORE switching the mode. The worker dispatches the two
  // concurrently, and applying a permission mode the provider cannot take live
  // relaunches the agent -- a relaunch that won the race killed the session
  // before the allow reached it, so the tool call was never answered.
  const handleAllow = async () => {
    await sendResponse(props.request.agentId, props.onRespond, buildAllowResponse(props.request.requestId, getToolInput(props.request.payload)))
    if (bypass() && props.bypass)
      await props.bypass.apply(props.bypass.settings)
  }

  return (
    <ControlDecisionFooter
      hasEditorContent={props.hasEditorContent}
      onSendFeedback={props.onTriggerSend}
      negativeAction={{ label: 'Deny', testId: 'control-deny-btn', onSelect: handleDeny }}
      positiveAction={{ label: 'Allow', testId: 'control-allow-btn', onSelect: handleAllow }}
      switches={() => props.bypass
        ? [{
            id: 'control-bypass-permissions-checkbox',
            label: 'Bypass Permissions',
            checked: bypass(),
            onChange: setBypass,
          }]
        : []}
    />
  )
}
