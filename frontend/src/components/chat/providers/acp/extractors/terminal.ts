import { isObject, pickString } from '~/lib/jsonPick'

/**
 * First ACP tool-call content entry of `{ type: 'terminal', terminalId }`.
 * ACP agents embed this while a host terminal runs so the client can
 * correlate the tool call with the session. Returns null when absent.
 */
export function acpTerminalFromToolCallContent(
  content: unknown,
): { terminalId: string } | null {
  if (!Array.isArray(content))
    return null
  for (const entry of content) {
    if (!isObject(entry))
      continue
    const obj = entry as Record<string, unknown>
    if (obj.type !== 'terminal')
      continue
    const terminalId = pickString(obj, 'terminalId')
    if (!terminalId)
      continue
    return { terminalId }
  }
  return null
}
