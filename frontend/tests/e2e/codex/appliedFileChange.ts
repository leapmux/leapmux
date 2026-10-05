import type { AgentChatMessage } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { codexChangeKind } from '../../../src/components/chat/providers/codex/extractors/fileChange'
import { CODEX_ITEM } from '../../../src/generated/contracts/codex-protocol'
import { AgentProvider, MessageCompletion, MessageSource } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { nativeMessageBody } from '../helpers/nativeMessages'
import { nativeToolResult } from '../helpers/nativeToolResult'

/** Require the exact current Worker span and its native started and completed file items. */
export function codexAppliedFileChange(messages: readonly AgentChatMessage[], sessionId: string, itemId: string, path: string): Record<string, unknown> {
  if (!sessionId || !itemId || !path)
    throw new Error('The native Codex file proof requires its exact session, item ID, and path.')
  const matching = messages.filter(message => message.agentSessionId === sessionId && message.spanId === itemId)
  const items = matching.map((message) => {
    if (message.source !== MessageSource.AGENT || message.agentProvider !== AgentProvider.CODEX || message.spanType !== CODEX_ITEM.FileChange
      || message.id.trim() === '' || message.seq < 0n) {
      throw new Error('The exact Worker file span contains invalid native message metadata.')
    }
    const body = nativeMessageBody(message)
    if (!isObject(body) || body.threadId !== sessionId || typeof body.turnId !== 'string' || body.turnId.trim() === ''
      || !isObject(body.item) || body.item.id !== itemId || body.item.type !== CODEX_ITEM.FileChange) {
      throw new Error('The exact Worker file span contains an unrelated native item.')
    }
    return { message, turnId: body.turnId, item: body.item }
  })
  const started = items.filter(({ item }) => item.status === 'inProgress')
  const completed = items.filter(({ item }) => item.status === 'completed')
  if (items.length !== 2 || started.length !== 1 || completed.length !== 1)
    throw new Error('The Codex file item must have one actual start and one actual completion.')
  const start = started[0]
  const end = completed[0]
  if (!start || !end || start.message.id === end.message.id || start.message.seq >= end.message.seq)
    throw new Error('The native Codex file item completed before its actual start.')
  // The Worker stores each native item as Codex sent it (persistSharedItemCompleted),
  // so the row states no Worker completion: the native `status: completed` above is
  // the completion. Only a row that the Worker finishes itself, such as a tool
  // that a turn left open, carries a Worker completion.
  if (start.turnId !== end.turnId || end.message.completion !== MessageCompletion.UNSPECIFIED || !Array.isArray(end.item.changes))
    throw new Error('The native Codex file item did not report a completed applied change.')
  const changes = end.item.changes.filter(isObject).filter(change => change.path === path && codexChangeKind(change) === 'add')
  if (changes.length !== 1 || typeof changes[0]?.diff !== 'string')
    throw new Error('The native Codex completion contains no unique added-file diff for the exact path.')
  return changes[0]
}

/** Require the exact outer code-mode call to finish and return its native empty object. */
export function requireCodexPatchResult(request: MockModelRequestRecord, callId: string): void {
  const value: unknown = JSON.parse(nativeToolResult(request, callId))
  if (!Array.isArray(value))
    throw new Error('The native Codex patch result must contain its code-mode content blocks.')
  const blocks = value.filter(isObject).filter(block => block.type === 'input_text' && typeof block.text === 'string')
  if (!blocks.some(block => String(block.text).startsWith('Script completed\n')))
    throw new Error('The exact native Codex patch code cell did not finish successfully.')
  const returned = blocks.flatMap((block) => {
    try {
      const parsed: unknown = JSON.parse(String(block.text))
      return isObject(parsed) ? [parsed] : []
    }
    catch {
      return []
    }
  })
  if (returned.length !== 1 || Object.keys(returned[0] ?? {}).length !== 0)
    throw new Error('The exact native Codex patch call did not return its successful empty object.')
}
