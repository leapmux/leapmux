import type { Component } from 'solid-js'
import type { ActionsProps, ContentProps } from './types'

import { For, Show } from 'solid-js'
import { ButtonGroup } from '~/components/common/ButtonGroup'
import * as styles from '../ControlRequestBanner.css'
import { toRpcId } from './CodexControlRequest'
import { sendResponse } from './types'

interface GeminiPermissionOption {
  optionId: string
  kind: string
  name: string
}

function getGeminiParams(payload: Record<string, unknown>): Record<string, unknown> | undefined {
  return payload.params as Record<string, unknown> | undefined
}

function getToolCall(payload: Record<string, unknown>): Record<string, unknown> | undefined {
  return getGeminiParams(payload)?.toolCall as Record<string, unknown> | undefined
}

function getOptions(payload: Record<string, unknown>): GeminiPermissionOption[] {
  return (getGeminiParams(payload)?.options as GeminiPermissionOption[] | undefined) ?? []
}

function defaultAllowOptionId(payload: Record<string, unknown>): string | undefined {
  const options = getOptions(payload)
  return options.find(option => option.kind === 'allow_once')?.optionId
    ?? options.find(option => option.kind !== 'reject_once')?.optionId
}

export function sendGeminiPermissionResponse(
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

export const GeminiControlContent: Component<ContentProps> = (props) => {
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

export const GeminiControlActions: Component<ActionsProps> = (props) => {
  const options = () => getOptions(props.request.payload)

  const handleOption = (optionId: string) => {
    sendGeminiPermissionResponse(props.request.agentId, props.onRespond, props.request.requestId, optionId)
  }

  const handleBypassPermissions = () => {
    const allowOptionId = defaultAllowOptionId(props.request.payload)
    if (!allowOptionId)
      return
    sendGeminiPermissionResponse(props.request.agentId, props.onRespond, props.request.requestId, allowOptionId)
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
      </div>
    </div>
  )
}

// Provider-neutral aliases used by all ACP providers.
export { GeminiControlActions as ACPControlActions, GeminiControlContent as ACPControlContent }
