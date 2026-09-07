import type { Component } from 'solid-js'
import type { WirePermissionOption } from '../../controls/permissionOptions'
import type { ActionsProps, ContentProps } from '../../controls/types'

import { Show } from 'solid-js'
import * as styles from '../../ControlRequestBanner.css'
import { PermissionDecisionActions } from '../../controls/PermissionDecisionActions'
import { sendSelectedOptionResponse } from '../../controls/types'

function getACPParams(payload: Record<string, unknown>): Record<string, unknown> | undefined {
  return payload.params as Record<string, unknown> | undefined
}

function getToolCall(payload: Record<string, unknown>): Record<string, unknown> | undefined {
  return getACPParams(payload)?.toolCall as Record<string, unknown> | undefined
}

function getOptions(payload: Record<string, unknown>): WirePermissionOption[] {
  return (getACPParams(payload)?.options as WirePermissionOption[] | undefined) ?? []
}

export function sendACPPermissionResponse(
  onRespond: (content: Uint8Array) => Promise<void>,
  requestId: string,
  optionId: string,
): Promise<void> {
  return sendSelectedOptionResponse(onRespond, requestId, optionId)
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

export const ACPControlActions: Component<ActionsProps> = props => (
  <PermissionDecisionActions {...props} options={getOptions} send={sendACPPermissionResponse} />
)
