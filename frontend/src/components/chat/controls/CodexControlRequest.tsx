import type { Component } from 'solid-js'
import type { ActionsProps, ContentProps } from './types'

import { For, Show } from 'solid-js'
import { ButtonGroup } from '~/components/common/ButtonGroup'
import * as styles from '../ControlRequestBanner.css'
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
  const params = () => getCodexParams(props.request.payload)
  const method = () => props.request.payload.method as string | undefined
  const reason = () => params()?.reason as string | undefined
  const command = () => params()?.command as string | undefined
  const cwd = () => params()?.cwd as string | undefined

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
    <>
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
    </>
  )
}

/** Codex-specific control request action buttons. */
export const CodexControlActions: Component<ActionsProps> = (props) => {
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
  )
}
