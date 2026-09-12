import type { CommandResultEntry } from '../../../results/commandResult'
import { isObject, pickBoolean, pickNumber, pickObject, pickString } from '~/lib/jsonPick'

/**
 * First ACP tool-call content entry of `{ type: 'terminal', terminalId }`.
 * ACP agents embed this while a host terminal runs so the client can
 * correlate the tool call with the session. Returns null when absent.
 */
export function acpTerminalIds(
  content: unknown,
): string[] {
  if (!Array.isArray(content))
    return []
  const ids = new Set<string>()
  for (const entry of content) {
    if (!isObject(entry))
      continue
    const obj = entry as Record<string, unknown>
    if (obj.type !== 'terminal')
      continue
    const terminalId = pickString(obj, 'terminalId')
    if (!terminalId)
      continue
    ids.add(terminalId)
  }
  return [...ids]
}

/** Resolve only terminal IDs that this tool call actually carries. */
export function acpTerminalResults(tool: Record<string, unknown>, supplemental: Record<string, unknown> | undefined): { entries: CommandResultEntry[], unresolved: string[] } {
  const entries: CommandResultEntry[] = []
  const unresolved: string[] = []
  const terminals = pickObject(supplemental, 'terminals')
  for (const id of acpTerminalIds(tool.content)) {
    const result = pickObject(terminals, id)
    if (typeof result?.output !== 'string') {
      unresolved.push(id)
      continue
    }
    const exitCode = pickNumber(result, 'exitCode', undefined)
    entries.push({
      label: `Terminal ${id}`,
      source: {
        output: result.output,
        exitCode,
        truncated: pickBoolean(result, 'truncated') ?? false,
        interrupted: tool.status === 'cancelled',
        isError: exitCode !== undefined && exitCode !== 0,
      },
    })
  }
  return { entries, unresolved }
}
