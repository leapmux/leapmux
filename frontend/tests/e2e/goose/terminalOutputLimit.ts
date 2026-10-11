import { ACP_SUPPLEMENT, ACP_TERMINAL_RESULT } from '../../../src/generated/contracts/acp-protocol'
import { MESSAGE_SUPPLEMENT_FIELD } from '../../../src/generated/contracts/worker-vocab'
import { isObject } from '../../../src/lib/jsonPick'
import { acpClosedToolCall, requireAcpToolSupplement } from '../helpers/acpToolFrame'

/** Read the exact native client-terminal reference and its retained output. */
export function gooseTerminalOutputFileLimit(original: unknown, supplemental: unknown, callId: string): { callId: string, terminalId: string, text: string } {
  if (!isObject(original) || !acpClosedToolCall(original, callId, ['completed']))
    throw new Error('The native Goose terminal proof requires its exact completed call.')
  const retained = isObject(supplemental) ? supplemental[MESSAGE_SUPPLEMENT_FIELD.Provider] : undefined
  if (!isObject(retained))
    throw new Error('The native Goose terminal proof requires its retained protocol record.')
  const provider = requireAcpToolSupplement(original, retained, 'Goose terminal')
  const protocol = provider[ACP_SUPPLEMENT.Protocol]
  const terminals = provider[ACP_SUPPLEMENT.Terminals]
  const meta = isObject(protocol) && isObject(protocol._meta) ? protocol._meta : undefined
  const goose = isObject(meta?.goose) ? meta.goose : undefined
  const tool = isObject(goose?.toolCall) ? goose.toolCall : undefined
  const content = isObject(protocol) && Array.isArray(protocol.content) ? protocol.content : undefined
  const refs = content?.filter(isObject).filter(block => block.type === 'terminal') ?? []
  const ref = refs.length === 1 ? refs[0] : undefined
  const terminalId = typeof ref?.terminalId === 'string' ? ref.terminalId : undefined
  if (tool?.toolName !== 'shell' || tool.extensionName !== 'developer' || !terminalId || !isObject(terminals) || !Object.hasOwn(terminals, terminalId))
    throw new Error('The native Goose shell has no exact retained client-terminal reference.')
  const result = terminals[terminalId]
  const text = isObject(result) ? result[ACP_TERMINAL_RESULT.Output] : undefined
  if (!isObject(result) || typeof text !== 'string'
    || result[ACP_TERMINAL_RESULT.Truncated] !== true || result[ACP_TERMINAL_RESULT.ExitCode] !== 0) {
    throw new Error('The native Goose client-terminal result has another output or exit state.')
  }
  return { callId, terminalId, text }
}
