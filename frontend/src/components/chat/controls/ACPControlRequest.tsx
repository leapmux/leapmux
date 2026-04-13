import type { Component } from 'solid-js'
import type { ActionsProps, ContentProps } from './types'

import { For, Show } from 'solid-js'
import { ButtonGroup } from '~/components/common/ButtonGroup'
import { Tooltip } from '~/components/common/Tooltip'
import * as styles from '../ControlRequestBanner.css'
import { toRpcId } from './CodexControlRequest'
import { sendResponse } from './types'

interface ACPPermissionOption {
  optionId: string
  kind: string
  name: string
}

function getACPParams(payload: Record<string, unknown>): Record<string, unknown> | undefined {
  return payload.params as Record<string, unknown> | undefined
}

function getToolCall(payload: Record<string, unknown>): Record<string, unknown> | undefined {
  return getACPParams(payload)?.toolCall as Record<string, unknown> | undefined
}

function getOptions(payload: Record<string, unknown>): ACPPermissionOption[] {
  return (getACPParams(payload)?.options as ACPPermissionOption[] | undefined) ?? []
}

function defaultAllowOptionId(payload: Record<string, unknown>): string | undefined {
  const options = getOptions(payload)
  return options.find(option => option.kind === 'allow_once')?.optionId
    ?? options.find(option => option.kind !== 'reject_once')?.optionId
}

export function sendACPPermissionResponse(
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

export const ACPControlContent: Component<ContentProps> = (props) => {
  const toolCall = () => getToolCall(props.request.payload)
  const title = () => (toolCall()?.title as string) || 'Permission Request'
  const kind = () => toolCall()?.kind as string | undefined

  return (
    <>
      <div class={styles.controlBannerTitle}>{title()}</div>
      <Show when={kind()}>
        <div class={styles.codexCwd}>{kind()}</div>
      </Show>
    </>
  )
}

export const ACPControlActions: Component<ActionsProps> = (props) => {
  const options = () => getOptions(props.request.payload)

  const handleOption = (optionId: string) => {
    sendACPPermissionResponse(props.request.agentId, props.onRespond, props.request.requestId, optionId)
  }

  const handleBypassPermissions = () => {
    const allowOptionId = defaultAllowOptionId(props.request.payload)
    if (!allowOptionId)
      return
    sendACPPermissionResponse(props.request.agentId, props.onRespond, props.request.requestId, allowOptionId)
    if (props.bypassPermissionMode)
      props.onPermissionModeChange?.(props.bypassPermissionMode)
  }

  return (
    <div class={styles.controlFooter}>
      <div class={styles.controlFooterLeft}>
        {props.infoTrigger}
      </div>
      <div class={styles.controlFooterRight}>
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
          <Show when={props.bypassPermissionMode && defaultAllowOptionId(props.request.payload)}>
            <Tooltip text="Allow this request and stop asking for permissions">
              <button
                data-variant="secondary"
                onClick={handleBypassPermissions}
                data-testid="control-bypass-btn"
                aria-label="Allow this request and stop asking for permissions"
              >
                & Bypass Permissions
              </button>
            </Tooltip>
          </Show>
        </ButtonGroup>
      </div>
    </div>
  )
}
