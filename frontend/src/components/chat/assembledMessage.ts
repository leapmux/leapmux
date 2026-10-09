import { ASSEMBLED_MESSAGE } from '~/generated/contracts/worker-vocab'
import { MessageCompletion as ProtoMessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject, pickString } from '~/lib/jsonPick'

export type MessageCompletion = 'complete' | 'interrupted' | 'error' | 'finished'

export const INTERRUPTION_MARKER = 'Text truncated by interruption.'
export const ERROR_MARKER = 'Text truncated by an error.'
export const FINISHED_MARKER = 'Text ended without a known outcome.'

export interface AssembledMessage {
  kind: 'text' | 'reasoning' | 'plan'
  text: string
  completion: MessageCompletion
}

export function messageCompletionFromProto(value: ProtoMessageCompletion | undefined): MessageCompletion | null {
  switch (value) {
    case ProtoMessageCompletion.COMPLETE:
      return ASSEMBLED_MESSAGE.CompletionComplete
    case ProtoMessageCompletion.INTERRUPTED:
      return ASSEMBLED_MESSAGE.CompletionInterrupted
    case ProtoMessageCompletion.ERROR:
      return ASSEMBLED_MESSAGE.CompletionError
    case ProtoMessageCompletion.FINISHED:
      return ASSEMBLED_MESSAGE.CompletionFinished
    default:
      return null
  }
}

function parseCompletion(value: unknown): MessageCompletion | null {
  if (value !== ASSEMBLED_MESSAGE.CompletionComplete
    && value !== ASSEMBLED_MESSAGE.CompletionInterrupted
    && value !== ASSEMBLED_MESSAGE.CompletionError
    && value !== ASSEMBLED_MESSAGE.CompletionFinished) {
    return null
  }
  return value
}

export function parseAssembledMessage(value: unknown): AssembledMessage | null {
  if (!isObject(value) || value[ASSEMBLED_MESSAGE.FieldType] !== ASSEMBLED_MESSAGE.Type)
    return null
  const kind = pickString(value, ASSEMBLED_MESSAGE.FieldKind)
  const completion = parseCompletion(pickString(value, ASSEMBLED_MESSAGE.FieldCompletion))
  if (kind !== ASSEMBLED_MESSAGE.KindText
    && kind !== ASSEMBLED_MESSAGE.KindReasoning
    && kind !== ASSEMBLED_MESSAGE.KindPlan) {
    return null
  }
  if (!completion)
    return null
  return { kind, text: pickString(value, ASSEMBLED_MESSAGE.FieldText), completion }
}

export function completionMarker(completion: MessageCompletion | null): string | null {
  if (completion === ASSEMBLED_MESSAGE.CompletionInterrupted)
    return INTERRUPTION_MARKER
  if (completion === ASSEMBLED_MESSAGE.CompletionError)
    return ERROR_MARKER
  if (completion === ASSEMBLED_MESSAGE.CompletionFinished)
    return FINISHED_MARKER
  return null
}

export function appendCompletionMarker(text: string, completion: MessageCompletion | null): string {
  const marker = completionMarker(completion)
  if (!marker)
    return text
  return text ? `${text}\n\n${marker}` : marker
}

export function assembledMessageDisplayText(message: AssembledMessage): string {
  return appendCompletionMarker(message.text, message.completion)
}
