import type { Component } from 'solid-js'
import type { ActionsProps, ContentProps } from '../../controls/types'

import { For, Show } from 'solid-js'
import { ButtonGroup } from '~/components/common/ButtonGroup'
import { Tooltip } from '~/components/common/Tooltip'
import * as styles from '../../ControlRequestBanner.css'
import { ControlActionRow } from '../../controls/ControlActionRow'
import { sendResponse, toRpcId } from '../../controls/types'

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
        <div class={styles.bannerHint}>{kind()}</div>
      </Show>
    </>
  )
}

export const ACPControlActions: Component<ActionsProps> = (props) => {
  const options = () => getOptions(props.request.payload)

  const handleOption = (optionId: string) => {
    sendACPPermissionResponse(props.request.agentId, props.onRespond, props.request.requestId, optionId)
  }

  // Await the allow BEFORE switching the mode: the worker dispatches the two
  // concurrently, and a mode change the provider cannot take live relaunches the
  // agent, which kills the session before an un-awaited allow reaches it.
  const handleBypassPermissions = async () => {
    const allowOptionId = defaultAllowOptionId(props.request.payload)
    if (!allowOptionId)
      return
    await sendACPPermissionResponse(props.request.agentId, props.onRespond, props.request.requestId, allowOptionId)
    if (props.bypass)
      await props.bypass.apply(props.bypass.settings)
  }

  return (
    <ControlActionRow
      primary={(
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
          <Show when={props.bypass && defaultAllowOptionId(props.request.payload)}>
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
      )}
    />
  )
}
