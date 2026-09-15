import type { AssembledMessage, MessageCompletion } from '~/components/chat/assembledMessage'
import { ASSEMBLED_MESSAGE } from '~/generated/contracts/worker-vocab'

/**
 * Build one assembled-message row.
 *
 * This is the shape the worker writes when it joins a run of streamed text into a
 * single transcript row. LeapMux owns the envelope, so every provider that streams
 * text stores the same object and the shared classifier and renderer read it before
 * any provider plugin runs.
 */
export function assembledMessageRow(
  kind: AssembledMessage['kind'],
  text: string,
  completion: MessageCompletion = ASSEMBLED_MESSAGE.CompletionComplete,
): Record<string, unknown> {
  return {
    [ASSEMBLED_MESSAGE.FieldType]: ASSEMBLED_MESSAGE.Type,
    [ASSEMBLED_MESSAGE.FieldKind]: kind,
    [ASSEMBLED_MESSAGE.FieldText]: text,
    [ASSEMBLED_MESSAGE.FieldCompletion]: completion,
  }
}
