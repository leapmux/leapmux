import type { Component } from 'solid-js'
import type { ActionsProps } from './types'
import type { ControlRequest } from '~/stores/control.store'

import { Show } from 'solid-js'
import { apiCallTimeout } from '~/api/transport'
import { ButtonGroup } from '~/components/common/ButtonGroup'
import { waitForSettingsChanged } from '~/lib/settingsChangedEvent'
import { buildAllowResponse, getToolInput } from '~/utils/controlResponse'
import * as styles from '../ControlRequestBanner.css'
import { CollapsibleList } from './CollapsibleList'
import { sendResponse } from './types'

interface ToolGroup {
  tool: string
  prompts: string[]
}

/** Group permissions by tool name so related prompts are shown together. */
function groupByTool(prompts: Array<{ tool: string, prompt: string }>): ToolGroup[] {
  const map = new Map<string, string[]>()
  for (const p of prompts) {
    const list = map.get(p.tool)
    if (list) {
      list.push(p.prompt)
    }
    else {
      map.set(p.tool, [p.prompt])
    }
  }
  return Array.from(map, ([tool, prompts]) => ({ tool, prompts }))
}

export const ExitPlanModeContent: Component<{ request: ControlRequest }> = (props) => {
  const input = () => getToolInput(props.request.payload)
  const planSummary = () => {
    const prompts = input().allowedPrompts as Array<{ tool: string, prompt: string }> | undefined
    return prompts
  }
  const grouped = () => {
    const summary = planSummary()
    return summary ? groupByTool(summary) : []
  }

  return (
    <>
      <div class={styles.controlBannerTitle}>Plan Ready for Review</div>
      <Show when={planSummary()}>
        <div>
          <strong>Requested permissions:</strong>
          <ul>
            <CollapsibleList
              items={grouped()}
              maxVisible={3}
              moreLabel={n => `Show ${n} more group${n === 1 ? '' : 's'}\u2026`}
              renderItem={group => (
                <li>
                  {group.tool}
                  :
                  {' '}
                  {group.prompts.join(', ')}
                </li>
              )}
            />
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

  const handleBypassPermissions = async () => {
    // Approve the current request first.
    sendResponse(props.request.agentId, props.onRespond, buildAllowResponse(props.request.requestId))

    // The backend auto-sets permission mode to 'default' when ExitPlanMode
    // is approved. Wait for that settings_changed notification before
    // sending set_permission_mode, to avoid the race where our
    // bypassPermissions request gets overwritten by the auto-set.
    try {
      await waitForSettingsChanged(
        changes => changes.permissionMode?.old === 'plan',
        apiCallTimeout().timeoutMs ?? 15_000,
      )
    }
    catch {
      // On timeout, proceed anyway — degrade to old behavior rather than
      // silently dropping the user's intent.
    }

    props.onPermissionModeChange?.('bypassPermissions')
  }

  return (
    <div class={styles.controlFooter}>
      <div class={styles.controlFooterLeft}>
        {props.infoTrigger}
      </div>
      <div class={styles.controlFooterRight}>
        <button
          class="outline"
          onClick={handleReject}
          data-testid="plan-reject-btn"
        >
          Reject
        </button>
        <Show when={!props.hasEditorContent}>
          <ButtonGroup>
            <button
              onClick={handleApprove}
              data-testid="plan-approve-btn"
            >
              Approve
            </button>
            <button
              data-variant="secondary"
              onClick={handleBypassPermissions}
              data-testid="control-bypass-btn"
              title="Approve this plan and stop asking for permissions"
            >
              & Bypass Permissions
            </button>
          </ButtonGroup>
        </Show>
      </div>
    </div>
  )
}
