import type { CommandResult } from '../../../ir/commandResult'
import type { ACPToolSupplement } from '../toolSupplement'
import { ACP_BLOCK_TYPE, ACP_CONTENT_BLOCK } from '~/generated/contracts/acp-protocol'
import { isObject, pickString } from '~/lib/jsonPick'
import { acpSupplementTerminals } from '../toolSupplement'

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
    if (entry[ACP_CONTENT_BLOCK.Type] !== ACP_BLOCK_TYPE.Terminal)
      continue
    const terminalId = pickString(entry, ACP_CONTENT_BLOCK.TerminalID)
    if (!terminalId)
      continue
    ids.add(terminalId)
  }
  return [...ids]
}

/** Resolve only terminal IDs that this tool call actually carries. */
export function acpTerminalResults(tool: Record<string, unknown>, supplemental: ACPToolSupplement | undefined): { entries: CommandResult[], unresolved: string[] } {
  const entries: CommandResult[] = []
  const unresolved: string[] = []
  const terminals = acpSupplementTerminals(supplemental)
  for (const id of acpTerminalIds(tool[ACP_CONTENT_BLOCK.Content])) {
    const result = terminals.get(id)
    if (!result) {
      unresolved.push(id)
      continue
    }
    entries.push({
      label: `Terminal ${id}`,
      output: result.output,
      truncated: result.truncated,
      // Exactly one, because the worker derives them from one process state, and a
      // terminal still running states neither. One chained spread, so the exit the
      // type carries stays exclusive instead of two independent optional keys.
      ...(result.exitCode !== undefined ? { exitCode: result.exitCode } : result.signal !== undefined ? { signal: result.signal } : {}),
    })
  }
  return { entries, unresolved }
}
