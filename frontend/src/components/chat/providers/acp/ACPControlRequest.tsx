import type { Component } from 'solid-js'
import type { WirePermissionOption } from '../../controls/permissionOptionLabels'
import type { ActionsProps, ContentProps, ControlResponseSender } from '../../controls/types'

import { createEffect, createMemo, onCleanup, untrack } from 'solid-js'
import { pickObject, pickString } from '~/lib/jsonPick'
import { PermissionDecisionActions } from '../../controls/PermissionDecisionActions'
import { PermissionRequestContent } from '../../controls/PermissionRequestContent'
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
  onRespond: ControlResponseSender,
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
    return resolveACPToolCall(original, props.messageContext?.request({ spanId: pickString(original, 'toolCallId'), agentSessionId: props.request.agentSessionId ?? '' })?.parsed.parentObject)
  })
  const title = () => pickString(toolCall(), 'title') || pickString(toolCall(), 'kind')
  const kind = () => pickString(toolCall(), 'kind')
  createEffect(() => {
    const context = props.messageContext
    const id = pickString(getToolCall(props.request.payload), 'toolCallId')
    if (!context || !id)
      return
    const identity = { spanId: id, agentSessionId: props.request.agentSessionId ?? '' }
    onCleanup(context.retainSpan(identity))
    // A loaded request must not release its own lease and cause another fetch.
    if (!untrack(() => context.request(identity))) {
      void context.loadSpan(identity).catch((error: unknown) => {
        if (!(error instanceof DOMException && error.name === 'AbortError'))
          console.warn('Cannot load permission tool details', { id, error })
      })
    }
  })

  return (
    <PermissionRequestContent
      request={props.request}
      source={{
        title: title(),
        input: toolCall()?.rawInput,
        command: kind() === 'execute' ? pickString(pickObject(toolCall(), 'rawInput'), 'command', undefined) : undefined,
      }}
    />
  )
}

export const ACPControlActions: Component<ActionsProps> = props => (
  <PermissionDecisionActions {...props} options={getOptions} send={sendACPPermissionResponse} />
)
