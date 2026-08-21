import type { Component } from 'solid-js'
import type { ActionsProps } from './types'
import type { ControlRequest } from '~/stores/control.store'

import { Show } from 'solid-js'
import { ButtonGroup } from '~/components/common/ButtonGroup'
import { Tooltip } from '~/components/common/Tooltip'
import { keepFocusOnPress } from '~/lib/focusRetention'
import { buildAllowResponse, getToolInput, getToolName } from '~/utils/controlResponse'
import * as styles from '../ControlRequestBanner.css'
import { CollapsibleText } from './CollapsibleText'
import { ControlActionRow } from './ControlActionRow'
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
  const handleAllow = () => {
    sendResponse(props.request.agentId, props.onRespond, buildAllowResponse(props.request.requestId, getToolInput(props.request.payload)))
  }

  const handleDeny = () => {
    props.onTriggerSend()
  }

  // Await the allow BEFORE switching the mode. The worker dispatches the two
  // concurrently, and applying a permission mode the provider cannot take live
  // relaunches the agent -- a relaunch that won the race killed the session
  // before the allow reached it, so the tool call was never answered.
  const handleBypassPermissions = async () => {
    await sendResponse(props.request.agentId, props.onRespond, buildAllowResponse(props.request.requestId, getToolInput(props.request.payload)))
    if (props.bypassPermissionMode)
      props.onPermissionModeChange?.(props.bypassPermissionMode)
  }

  // Reject sits beside Allow, in `primary`. It is a decision on the request, and
  // every other provider's row puts its deny button there -- several emit both
  // from one connected ButtonGroup, so the zones cannot be split by polarity
  // without moving the same button to the opposite end for one provider only.
  return (
    <ControlActionRow
      primary={(
        <>
          <button
            class="outline"
            onMouseDown={keepFocusOnPress}
            onClick={handleDeny}
            data-testid="control-deny-btn"
          >
            {props.hasEditorContent ? 'Send Feedback' : 'Reject'}
          </button>
          <Show when={!props.hasEditorContent}>
            <ButtonGroup>
              <button
                onClick={handleAllow}
                data-testid="control-allow-btn"
              >
                Allow
              </button>
              <Show when={props.bypassPermissionMode}>
                <Tooltip text="Allow this request and stop asking for permissions">
                  <button
                    data-variant="secondary"
                    onClick={handleBypassPermissions}
                    data-testid="control-bypass-btn"
                  >
                    & Bypass Permissions
                  </button>
                </Tooltip>
              </Show>
            </ButtonGroup>
          </Show>
        </>
      )}
    />
  )
}
