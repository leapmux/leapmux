import type { ControlPayloadFault, ControlRequest } from '~/stores/control.store'
import { ControlResponseState } from '~/generated/proto/leapmux/v1/agent_pb'

export function canAnswerControlRequest(request: ControlRequest | null | undefined): boolean {
  return request?.responseState === undefined || request.responseState === ControlResponseState.READY
}

/**
 * What the banner says about a request whose bytes LeapMux could not read.
 *
 * The provider plugins all draw "Permission Required" from an empty payload, because an
 * empty object is what a permission request looks like once its fields are gone. That
 * sentence states what the agent asked for, and here nobody knows -- so the banner states
 * the fault instead, and offers no decision. See RL-002.
 */
export function controlPayloadFaultNotice(fault: ControlPayloadFault | undefined): string {
  switch (fault) {
    case 'malformed':
      return 'LeapMux cannot read this request. The agent sent bytes that are not JSON.'
    case 'not-an-object':
      return 'LeapMux cannot read this request. The agent sent JSON that is not an object.'
    default:
      return ''
  }
}

export function controlResponseStateNotice(state: ControlResponseState | undefined): string {
  switch (state) {
    case ControlResponseState.DELIVERED:
      return 'The response action is complete. Save its transcript entry without repeating the action.'
    case ControlResponseState.UNCERTAIN:
      return 'LeapMux cannot confirm response delivery. It will not send another response.'
    case ControlResponseState.PENDING:
      return 'LeapMux has a saved response but no delivery receipt yet.'
    case ControlResponseState.UNSPECIFIED:
      return 'Check the response state before sending an answer.'
    case ControlResponseState.COMPLETED:
      return 'The response is complete.'
    case ControlResponseState.CANCELED:
      return 'This request is no longer pending.'
    default:
      return ''
  }
}
