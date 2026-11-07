import type { AgentControlRequest } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { AgentEvent } from '../../../src/generated/proto/leapmux/v1/watch_pb'
import type { AgentWatchServer } from './agentEventWatch'
import { isObject } from '../../../src/lib/jsonPick'
import { watchAgentEvents } from './agentEventWatch'

export interface NativeControlFrame {
  requestId: string
  payload: Record<string, unknown>
  canceled?: boolean
  responseState: AgentControlRequest['responseState']
  claimToken?: string
}

/**
 * The native control request, changed response state, or cancellation that one
 * event carries. A cancellation has no payload. Any other event, and a
 * response-state change with no payload, gives undefined. A malformed control
 * fails the watch.
 */
export function nativeControlFrame(event: AgentEvent): NativeControlFrame | undefined {
  const inner = event.event
  if (inner.case === 'controlCancel') {
    if (!inner.value.requestId)
      throw new Error('The native control frame has no request ID.')
    return {
      requestId: inner.value.requestId,
      payload: {},
      canceled: true,
      responseState: inner.value.responseState,
      ...(inner.value.claimToken
        ? { claimToken: inner.value.claimToken }
        : {}),
    }
  }
  if (inner.case !== 'controlRequest' && inner.case !== 'controlResponseChanged')
    return undefined
  if (inner.case === 'controlResponseChanged' && inner.value.payload.length === 0)
    return undefined
  try {
    if (!inner.value.requestId)
      throw new Error('The native control frame has no request ID.')
    const parsed: unknown = JSON.parse(new TextDecoder().decode(inner.value.payload))
    if (!isObject(parsed))
      throw new Error('The native control payload is not an object.')
    return {
      requestId: inner.value.requestId,
      payload: parsed,
      responseState: inner.value.responseState,
      ...(inner.value.claimToken
        ? { claimToken: inner.value.claimToken }
        : {}),
    }
  }
  catch (error) {
    throw new Error('The Worker sent an invalid native control frame.', { cause: error })
  }
}

/** Capture real provider controls before a native operation starts. */
export async function watchNativeControls(
  server: AgentWatchServer,
  agentId: string,
): Promise<{ controls: () => readonly NativeControlFrame[], cancel: () => void }> {
  const watch = await watchAgentEvents(server, agentId, { label: 'native control subscription', select: nativeControlFrame })
  return { controls: watch.items, cancel: watch.cancel }
}
