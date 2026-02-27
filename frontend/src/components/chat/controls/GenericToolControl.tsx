import type { Component } from 'solid-js'
import type { ActionsProps } from './types'
import type { ControlRequest } from '~/stores/control.store'

import { Show } from 'solid-js'
import { ButtonGroup } from '~/components/common/ButtonGroup'
import { buildAllowResponse, getToolInput, getToolName } from '~/utils/controlResponse'
import * as styles from '../ControlRequestBanner.css'
import { CollapsibleText } from './CollapsibleText'
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
      <CollapsibleText text={inputSummary()} maxLines={6} class={styles.toolSummary} />
    </>
  )
}

export const GenericToolActions: Component<ActionsProps> = (props) => {
  const handleAllow = () => {
    sendResponse(props.request.agentId, props.onRespond, buildAllowResponse(props.request.requestId))
  }

  const handleDeny = () => {
    props.onTriggerSend()
  }

  const handleBypassPermissions = () => {
    // Allow the current request first, then switch to bypass mode.
    sendResponse(props.request.agentId, props.onRespond, buildAllowResponse(props.request.requestId))
    props.onPermissionModeChange?.('bypassPermissions')
  }

  return (
    <Show
      when={!props.hasEditorContent}
      fallback={(
        <button
          class="outline"
          onClick={handleDeny}
          data-testid="control-deny-btn"
        >
          Send Feedback
        </button>
      )}
    >
      <ButtonGroup>
        <button
          onClick={handleAllow}
          data-testid="control-allow-btn"
        >
          Allow
        </button>
        <button
          data-variant="secondary"
          onClick={handleBypassPermissions}
          data-testid="control-bypass-btn"
          title="Allow this request and stop asking for permissions"
        >
          & Bypass Permissions
        </button>
      </ButtonGroup>
    </Show>
  )
}
