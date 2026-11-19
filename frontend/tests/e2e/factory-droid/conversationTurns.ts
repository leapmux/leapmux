import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { NativeModelTurn } from '../helpers/nativeScenario'
import { isObject } from '../../../src/lib/jsonPick'
import { nativeModelConversationTurns } from '../helpers/nativeScenario'

const DISK_REMINDER_START = '<system-reminder>Disk space is low on this computer. Directories checked: ['
const REMINDER_END = '</system-reminder>'

/** Droid appends its disk warning as a standalone user-role string after the prompt. */
export function droidConversationTurns(request: MockModelRequestRecord): NativeModelTurn[] {
  if (request.protocol !== 'openai-chat-completions' || !isObject(request.body) || !Array.isArray(request.body.messages))
    return nativeModelConversationTurns(request)
  const messages = request.body.messages.filter((row: unknown) => {
    if (!isObject(row) || row.role !== 'user' || typeof row.content !== 'string')
      return true
    return !row.content.startsWith(DISK_REMINDER_START) || !row.content.endsWith(REMINDER_END)
      || row.content.indexOf(REMINDER_END) !== row.content.length - REMINDER_END.length
  })
  return nativeModelConversationTurns({
    ...request,
    body: { ...request.body, messages },
  })
}
