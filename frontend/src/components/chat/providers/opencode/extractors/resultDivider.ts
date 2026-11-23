import type { TurnEnd } from '../../../model/divider'
import { OPENCODE_NATIVE_EVENT, OPENCODE_NATIVE_MESSAGE_TYPE, OPENCODE_NATIVE_OUTCOME } from '~/generated/contracts/opencode-native-protocol'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { turnEndLabel } from '../../../turnEndLabel'
import { openCodeErrorText } from './notification'

/** Read the native event or projected idle record that finished a Worker turn. */
export function openCodeResultDivider(parsed: unknown, completion?: MessageCompletion): TurnEnd | null {
  if (!isObject(parsed))
    return null
  const type = pickString(parsed, 'type')
  let outcome: string
  let data: Record<string, unknown>
  if (type === OPENCODE_NATIVE_MESSAGE_TYPE.Idle) {
    data = parsed
    outcome = pickString(parsed, 'outcome')
  }
  else {
    data = pickObject(parsed, 'data') ?? {}
    switch (type) {
      case OPENCODE_NATIVE_EVENT.ExecutionSucceeded:
        outcome = OPENCODE_NATIVE_OUTCOME.Succeeded
        break
      case OPENCODE_NATIVE_EVENT.ExecutionFailed:
        outcome = OPENCODE_NATIVE_OUTCOME.Failed
        break
      case OPENCODE_NATIVE_EVENT.ExecutionInterrupted:
        outcome = OPENCODE_NATIVE_OUTCOME.Interrupted
        break
      default:
        return null
    }
  }
  if (completion === MessageCompletion.INTERRUPTED || outcome === OPENCODE_NATIVE_OUTCOME.Interrupted) {
    return {
      label: turnEndLabel('interrupted'),
      interrupted: true,
    }
  }
  if (outcome === OPENCODE_NATIVE_OUTCOME.Succeeded) {
    return {
      label: turnEndLabel('ended'),
    }
  }
  if (outcome !== OPENCODE_NATIVE_OUTCOME.Failed)
    return null
  const error = openCodeErrorText(pickObject(data, 'error'))
  return {
    label: turnEndLabel('failed', {
      ...(error.type
        ? {
            qualifiers: [error.type],
          }
        : {}),
      ...(error.message
        ? { reason: error.message }
        : {}),
    }),
    isError: true,
  }
}
