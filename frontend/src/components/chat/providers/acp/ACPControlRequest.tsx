import type { Component } from 'solid-js'
import type { WirePermissionOption } from '../../controls/permissionOptions'
import type { ActionsProps, ContentProps } from '../../controls/types'

import { createEffect, createMemo, onCleanup, Show, untrack } from 'solid-js'
import { pickObject, pickString } from '~/lib/jsonPick'
import * as styles from '../../ControlRequestBanner.css'
import { ControlJson } from '../../controls/ControlJson'
import { PermissionDecisionActions } from '../../controls/PermissionDecisionActions'
import { sendSelectedOptionResponse } from '../../controls/types'
import { resolveACPToolCall } from './toolPresentation'

function getACPParams(payload: Record<string, unknown>): Record<string, unknown> | undefined {
  return pickObject(payload, 'params') ?? undefined
}

function getToolCall(payload: Record<string, unknown>): Record<string, unknown> | undefined {
  return pickObject(getACPParams(payload), 'toolCall') ?? undefined
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
  const toolCall = createMemo(() => {
    const original = getToolCall(props.request.payload)
    if (!original)
      return undefined
    return resolveACPToolCall(original, props.messageContext?.request(pickString(original, 'toolCallId'))?.parsed.parentObject)
  })
  const title = () => pickString(toolCall(), 'title') || 'Permission Request'
  const kind = () => pickString(toolCall(), 'kind')
  createEffect(() => {
    const context = props.messageContext
    const id = pickString(getToolCall(props.request.payload), 'toolCallId')
    if (!context || !id)
      return
    onCleanup(context.retainSpan(id))
    // A loaded request must not release its own lease and cause another fetch.
    if (!untrack(() => context.request(id))) {
      void context.loadSpan(id).catch((error: unknown) => {
        if (!(error instanceof DOMException && error.name === 'AbortError'))
          console.warn('Cannot load permission tool details', { id, error })
      })
    }
  })

  return (
    <>
      <div class={styles.controlBannerTitle}>{title()}</div>
      <Show when={kind()}>
        <div class={styles.bannerHint}>{kind()}</div>
      </Show>
      <ControlJson value={toolCall()?.rawInput} hideEmpty />
    </>
  )
}

export const ACPControlActions: Component<ActionsProps> = props => (
  <PermissionDecisionActions {...props} options={getOptions} send={sendACPPermissionResponse} />
)
