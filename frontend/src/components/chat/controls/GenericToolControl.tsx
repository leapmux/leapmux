import type { Component } from 'solid-js'
import type { ActionsProps } from './types'
import type { ControlRequest } from '~/stores/control.store'

import { buildAllowResponse, getToolInput, getToolName } from '~/utils/controlResponse'
import * as styles from '../ControlRequestBanner.css'
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
      <pre class={styles.toolSummary}>{inputSummary()}</pre>
    </>
  )
}

export const GenericToolActions: Component<ActionsProps> = (props) => {
  const handleClick = () => {
    if (props.hasEditorContent) {
      // Editor text is used as deny comment via onSend handler
      props.onTriggerSend()
    }
    else {
      sendResponse(props.request.agentId, props.onRespond, buildAllowResponse(props.request.requestId))
    }
  }

  return (
    <button
      class={props.hasEditorContent ? 'outline' : undefined}
      onClick={handleClick}
      data-testid={props.hasEditorContent ? 'control-deny-btn' : 'control-allow-btn'}
    >
      {props.hasEditorContent ? 'Deny' : 'Allow'}
    </button>
  )
}
