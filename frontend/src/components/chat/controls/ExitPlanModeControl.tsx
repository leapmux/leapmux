import type { Component } from 'solid-js'
import type { ActionsProps } from './types'
import type { ControlRequest } from '~/stores/control.store'

import { createSignal, Show } from 'solid-js'
import { computePercentage } from '~/components/chat/ContextUsageGrid'
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
  const [clearContext, setClearContext] = createSignal(false)
  const [bypassPermissions, setBypassPermissions] = createSignal(false)
  const contextPct = () => {
    const pct = computePercentage(props.contextUsage, props.modelContextWindow, props.agentProvider)
    return pct !== null ? Math.round(pct) : null
  }

  const handleReject = () => {
    // Editor text is used as reject comment via onSend handler
    props.onTriggerSend()
  }

  const handleApprove = () => {
    const permMode = bypassPermissions() ? (props.bypassPermissionMode || 'bypassPermissions') : undefined
    sendResponse(props.request.agentId, props.onRespond, buildAllowResponse(props.request.requestId, getToolInput(props.request.payload), permMode, clearContext()))
  }

  return (
    <div class={styles.controlFooter}>
      <div class={styles.controlFooterLeft}>
        {props.infoTrigger}
      </div>
      <div class={styles.controlFooterRight}>
        <div>
          <Show when={contextPct() !== null}>
            <label data-testid="plan-clear-context-checkbox">
              <input
                type="checkbox"
                role="switch"
                checked={clearContext()}
                onChange={e => setClearContext(e.currentTarget.checked)}
              />
              Clear Context (
              {contextPct()}
              %)
            </label>
          </Show>
          <Show when={props.bypassPermissionMode}>
            <label data-testid="plan-bypass-permissions-checkbox">
              <input
                type="checkbox"
                role="switch"
                checked={bypassPermissions()}
                onChange={e => setBypassPermissions(e.currentTarget.checked)}
              />
              Bypass Permissions
            </label>
          </Show>
        </div>
        <button
          class="outline"
          onClick={handleReject}
          data-testid="plan-reject-btn"
        >
          {props.hasEditorContent ? 'Send Feedback' : 'Reject'}
        </button>
        <Show when={!props.hasEditorContent}>
          <button
            onClick={handleApprove}
            data-testid="plan-approve-btn"
          >
            Approve
          </button>
        </Show>
      </div>
    </div>
  )
}
