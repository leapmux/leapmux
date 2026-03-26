import type { Component } from 'solid-js'
import type { ActionsProps, ContentProps } from './types'

import { For, Show } from 'solid-js'
import { ButtonGroup } from '~/components/common/ButtonGroup'
import * as styles from '../ControlRequestBanner.css'
import { toRpcId } from './CodexControlRequest'
import { sendResponse } from './types'

/** Extract OpenCode requestPermission params from the control request payload. */
function getOpenCodeParams(payload: Record<string, unknown>): Record<string, unknown> | undefined {
  return payload.params as Record<string, unknown> | undefined
}

/** Extract the tool call info from a requestPermission payload. */
function getToolCall(payload: Record<string, unknown>): Record<string, unknown> | undefined {
  const params = getOpenCodeParams(payload)
  return params?.toolCall as Record<string, unknown> | undefined
}

/** Extract permission options from a requestPermission payload. */
function getOptions(payload: Record<string, unknown>): Array<{ optionId: string, kind: string, name: string }> {
  const params = getOpenCodeParams(payload)
  return (params?.options as Array<{ optionId: string, kind: string, name: string }>) ?? []
}

/**
 * Sends an OpenCode permission response as a JSON-RPC response.
 */
export function sendOpenCodePermissionResponse(
  agentId: string,
  onRespond: (agentId: string, content: Uint8Array) => Promise<void>,
  requestId: string,
  optionId: string,
): Promise<void> {
  return sendResponse(agentId, onRespond, {
    jsonrpc: '2.0',
    id: toRpcId(requestId),
    result: { outcome: { outcome: 'selected', optionId } },
  })
}

/** OpenCode-specific control request content. */
export const OpenCodeControlContent: Component<ContentProps> = (props) => {
  const toolCall = () => getToolCall(props.request.payload)
  const title = () => (toolCall()?.title as string) || 'Permission Request'
  const kind = () => toolCall()?.kind as string | undefined

  return (
    <div>
      <div class={styles.controlBannerTitle}>{title()}</div>
      <Show when={kind()}>
        <div class={styles.codexCwd}>{kind()}</div>
      </Show>
    </div>
  )
}

/** OpenCode-specific control request action buttons. */
export const OpenCodeControlActions: Component<ActionsProps> = (props) => {
  const options = () => getOptions(props.request.payload)

  const handleOption = (optionId: string) => {
    sendOpenCodePermissionResponse(props.request.agentId, props.onRespond, props.request.requestId, optionId)
  }

  const handleBypassPermissions = () => {
    // Allow once, then switch to bypass mode.
    sendOpenCodePermissionResponse(props.request.agentId, props.onRespond, props.request.requestId, 'once')
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
          when={options().length > 0}
          fallback={(
            <ButtonGroup>
              <button class="outline" onClick={() => handleOption('reject')} data-testid="control-deny-btn">Reject</button>
              <button onClick={() => handleOption('once')} data-testid="control-allow-btn">Allow once</button>
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
          <ButtonGroup>
            <For each={options()}>
              {option => (
                <button
                  class={option.kind === 'reject_once' ? 'outline' : undefined}
                  onClick={() => handleOption(option.optionId)}
                  data-testid={`control-decision-${option.optionId}`}
                >
                  {option.name}
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
        </Show>
      </div>
    </div>
  )
}
