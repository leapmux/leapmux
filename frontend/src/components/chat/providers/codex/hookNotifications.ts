import { CODEX_METHOD } from '~/generated/contracts/codex-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'

/** Whether a hook completion states a failure or an unknown future outcome. */
export function codexHookIsFailureOrUnknown(message: Record<string, unknown>): boolean {
  if (message.method !== CODEX_METHOD.HookCompleted)
    return false
  const run = pickObject(pickObject(message, 'params'), 'run')
  return pickString(run, 'status') !== 'completed'
}
