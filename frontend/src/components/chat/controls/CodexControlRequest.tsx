/* eslint-disable solid/no-innerhtml -- markdown is produced from controlled plan text via remark */
import type { Component } from 'solid-js'
import type { ActionsProps, ContentProps } from './types'

import { For, Match, Show, Switch } from 'solid-js'
import { ButtonGroup } from '~/components/common/ButtonGroup'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { buildAllowResponse, buildDenyResponse, getToolInput, getToolName } from '~/utils/controlResponse'
import * as styles from '../ControlRequestBanner.css'
import { markdownContent } from '../markdownContent.css'
import { CollapsibleText } from './CollapsibleText'
import { sendResponse } from './types'

/** Extract Codex approval params from the control request payload. */
function getCodexParams(payload: Record<string, unknown>): Record<string, unknown> | undefined {
  return payload.params as Record<string, unknown> | undefined
}

type CodexDecision = string | Record<string, unknown>

/**
 * Sends a Codex-native approval decision through the control response pipeline.
 * The `codexDecision` field is recognized by codex.tsx's buildControlResponse
 * and forwarded as-is to the JSON-RPC response.
 */
function sendCodexDecision(
  agentId: string,
  onRespond: (agentId: string, content: Uint8Array) => Promise<void>,
  requestId: string,
  decision: CodexDecision,
): Promise<void> {
  return sendResponse(agentId, onRespond, {
    type: 'control_response',
    response: {
      subtype: 'success',
      request_id: requestId,
      response: { codexDecision: decision },
    },
  })
}

function sendCodexPlanPromptResponse(
  agentId: string,
  onRespond: (agentId: string, content: Uint8Array) => Promise<void>,
  response: Record<string, unknown>,
): Promise<void> {
  return sendResponse(agentId, onRespond, {
    ...response,
    codexPlanModePrompt: true,
  })
}

/** Label for a Codex decision. */
function decisionLabel(decision: CodexDecision): string {
  if (typeof decision === 'string') {
    switch (decision) {
      case 'accept': return 'Allow'
      case 'acceptForSession': return 'Allow for Session'
      case 'decline': return 'Reject'
      case 'cancel': return 'Cancel'
      default: return decision
    }
  }
  if ('acceptWithExecpolicyAmendment' in decision)
    return 'Allow & Remember'
  if ('applyNetworkPolicyAmendment' in decision)
    return 'Apply Network Policy'
  return 'Allow'
}

/** Whether a decision is a cancel/decline type (rendered as outline button). */
function isNegativeDecision(decision: CodexDecision): boolean {
  return decision === 'decline' || decision === 'cancel'
}

/** Codex-specific control request content. */
export const CodexControlContent: Component<ContentProps> = (props) => {
  const toolName = () => getToolName(props.request.payload)
  const params = () => getCodexParams(props.request.payload)
  const method = () => props.request.payload.method as string | undefined
  const reason = () => params()?.reason as string | undefined
  const command = () => params()?.command as string | undefined
  const cwd = () => params()?.cwd as string | undefined
  const plan = () => getToolInput(props.request.payload).plan as string | undefined

  const title = () => {
    const m = method()
    if (m === 'item/commandExecution/requestApproval')
      return 'Command Execution'
    if (m === 'item/fileChange/requestApproval')
      return 'File Change'
    if (m === 'item/permissions/requestApproval')
      return 'Permission Request'
    return 'Approval Required'
  }

  return (
    <Switch>
      <Match when={toolName() === 'CodexPlanModePrompt'}>
        <div class={styles.controlBannerTitle}>Implement this plan?</div>
        <Show when={plan()}>
          <div class={markdownContent} innerHTML={renderMarkdown(plan()!)} />
        </Show>
      </Match>
      <Match when={true}>
        <div class={styles.controlBannerTitle}>{title()}</div>
        <Show when={reason()}>
          <div class={styles.codexReason}>{reason()}</div>
        </Show>
        <Show when={command()}>
          <CollapsibleText text={command()!} maxLines={6} class={styles.toolSummary} />
        </Show>
        <Show when={cwd()}>
          <div class={styles.codexCwd}>
            {'cwd: '}
            {cwd()}
          </div>
        </Show>
      </Match>
    </Switch>
  )
}

/** Codex-specific control request action buttons. */
export const CodexControlActions: Component<ActionsProps> = (props) => {
  const toolName = () => getToolName(props.request.payload)
  const params = () => getCodexParams(props.request.payload)
  const availableDecisions = () => params()?.availableDecisions as CodexDecision[] | undefined

  const handleDecision = (decision: CodexDecision) => {
    sendCodexDecision(props.request.agentId, props.onRespond, props.request.requestId, decision)
  }

  const handleBypassPermissions = () => {
    // Accept the current request, then switch to bypass mode.
    sendCodexDecision(props.request.agentId, props.onRespond, props.request.requestId, 'accept')
    if (props.bypassPermissionMode)
      props.onPermissionModeChange?.(props.bypassPermissionMode)
  }

  return (
    <Switch>
      <Match when={toolName() === 'CodexPlanModePrompt'}>
        <div class={styles.controlFooter}>
          <div class={styles.controlFooterLeft}>
            {props.infoTrigger}
          </div>
          <div class={styles.controlFooterRight}>
            <ButtonGroup>
              <button
                class="outline"
                onClick={() => {
                  if (props.hasEditorContent) {
                    props.onTriggerSend()
                    return
                  }
                  sendCodexPlanPromptResponse(props.request.agentId, props.onRespond, buildDenyResponse(props.request.requestId, ''))
                }}
                data-testid="control-deny-btn"
              >
                {props.hasEditorContent ? 'Send Feedback' : 'Stay in Plan Mode'}
              </button>
              <button
                onClick={() => sendCodexPlanPromptResponse(props.request.agentId, props.onRespond, buildAllowResponse(props.request.requestId))}
                data-testid="control-allow-btn"
              >
                Implement Plan
              </button>
            </ButtonGroup>
          </div>
        </div>
      </Match>
      <Match when={true}>
        <div class={styles.controlFooter}>
          <div class={styles.controlFooterLeft}>
            {props.infoTrigger}
          </div>
          <div class={styles.controlFooterRight}>
            <Show
              when={availableDecisions()}
              fallback={(
                <ButtonGroup>
                  <button class="outline" onClick={() => handleDecision('cancel')} data-testid="control-deny-btn">Cancel</button>
                  <button onClick={() => handleDecision('accept')} data-testid="control-allow-btn">Allow</button>
                  <Show when={props.bypassPermissionMode}>
                    <button
                      data-variant="secondary"
                      onClick={handleBypassPermissions}
                      data-testid="control-bypass-btn"
                      title="Allow this request and stop asking for permissions"
                    >
                      & Bypass Permissions
                    </button>
                  </Show>
                </ButtonGroup>
              )}
            >
              {decisions => (
                <ButtonGroup>
                  <For each={decisions()}>
                    {decision => (
                      <button
                        class={isNegativeDecision(decision) ? 'outline' : undefined}
                        onClick={() => handleDecision(decision)}
                        data-testid={`control-decision-${typeof decision === 'string' ? decision : Object.keys(decision)[0]}`}
                      >
                        {decisionLabel(decision)}
                      </button>
                    )}
                  </For>
                  <Show when={props.bypassPermissionMode}>
                    <button
                      data-variant="secondary"
                      onClick={handleBypassPermissions}
                      data-testid="control-bypass-btn"
                      title="Allow this request and stop asking for permissions"
                    >
                      & Bypass Permissions
                    </button>
                  </Show>
                </ButtonGroup>
              )}
            </Show>
          </div>
        </div>
      </Match>
    </Switch>
  )
}
