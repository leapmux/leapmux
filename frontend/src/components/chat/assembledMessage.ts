import { ASSEMBLED_MESSAGE } from '~/generated/contracts/worker-vocab'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

export type MessageCompletion = 'complete' | 'interrupted' | 'error'

export const INTERRUPTION_MARKER = 'Text truncated by interruption.'
export const ERROR_MARKER = 'Text truncated by an error.'

export interface AssembledMessage {
  kind: 'text' | 'reasoning' | 'plan'
  text: string
  completion: MessageCompletion
}

function parseCompletion(value: unknown): MessageCompletion | null {
  if (value !== ASSEMBLED_MESSAGE.CompletionComplete
    && value !== ASSEMBLED_MESSAGE.CompletionInterrupted
    && value !== ASSEMBLED_MESSAGE.CompletionError) {
    return null
  }
  return value
}

export function parseAssembledMessage(value: unknown): AssembledMessage | null {
  if (!isObject(value) || value.type !== ASSEMBLED_MESSAGE.Type)
    return null
  const kind = pickString(value, 'kind')
  const completion = parseCompletion(pickString(value, 'completion'))
  if (kind !== ASSEMBLED_MESSAGE.KindText
    && kind !== ASSEMBLED_MESSAGE.KindReasoning
    && kind !== ASSEMBLED_MESSAGE.KindPlan) {
    return null
  }
  if (!completion)
    return null
  return { kind, text: pickString(value, 'text'), completion }
}

export function parseProviderMessageCompletion(value: unknown): MessageCompletion | null {
  if (!isObject(value))
    return null
  return parseCompletion(pickString(pickObject(value, ASSEMBLED_MESSAGE.MetadataKey), 'completion'))
}

export function completionMarker(completion: MessageCompletion | null): string | null {
  if (completion === ASSEMBLED_MESSAGE.CompletionInterrupted)
    return INTERRUPTION_MARKER
  if (completion === ASSEMBLED_MESSAGE.CompletionError)
    return ERROR_MARKER
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
