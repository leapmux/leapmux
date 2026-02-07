import type { Component } from 'solid-js'
import type { ActionsProps } from './types'
import type { ControlRequest } from '~/stores/control.store'

import { For, Show } from 'solid-js'
import { buildAllowResponse, getToolInput } from '~/utils/controlResponse'
import * as styles from '../ControlRequestBanner.css'
import { sendResponse } from './types'

export const ExitPlanModeContent: Component<{ request: ControlRequest }> = (props) => {
  const input = () => getToolInput(props.request.payload)
  const planSummary = () => {
    const prompts = input().allowedPrompts as Array<{ tool: string, prompt: string }> | undefined
    return prompts
  }

  return (
    <>
      <div class={styles.controlBannerTitle}>Plan Ready for Review</div>
      <Show when={planSummary()}>
        <div>
          <strong>Requested permissions:</strong>
          <ul>
            <For each={planSummary()!}>
              {p => (
                <li>
                  {p.tool}
                  :
                  {' '}
                  {p.prompt}
                </li>
              )}
            </For>
          </ul>
        </div>
      </Show>
      <Show when={!planSummary()}>
        <div>The agent has finished planning and is ready to proceed.</div>
      </Show>
    </>
  )
}

export const ExitPlanModeActions: Component<ActionsProps> = (props) => {
  const handleReject = () => {
    // Editor text is used as reject comment via onSend handler
    props.onTriggerSend()
  }

  const handleApprove = () => {
    sendResponse(props.request.agentId, props.onRespond, buildAllowResponse(props.request.requestId))
  }

  return (
    <div class={styles.controlFooter}>
      <div class={styles.controlFooterLeft}>
        <button
          class="outline"
          onClick={handleReject}
          data-testid="plan-reject-btn"
        >
          Reject
        </button>
        {props.infoTrigger}
      </div>
      <div class={styles.controlFooterRight}>
        <button
          onClick={handleApprove}
          disabled={props.hasEditorContent}
          data-testid="plan-approve-btn"
        >
          Approve
        </button>
      </div>
    </div>
  )
}
