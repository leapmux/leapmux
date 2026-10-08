import type { NativeScenarioContext } from './nativeScenario'
import { selectedAgentTabId } from './nativeScenario'

/** Resolve only browser identity. A provider without a resolver keeps its original ID and performs no lookup. */
export async function nativeToolRowId(context: Pick<NativeScenarioContext, 'page' | 'resolveToolRowId'>, callId: string): Promise<string> {
  if (!context.resolveToolRowId)
    return callId
  if (callId.trim() === '')
    throw new Error('The native tool row query requires a model call ID.')
  const agentId = await selectedAgentTabId(context.page)
  const rowId = await context.resolveToolRowId({ callId, agentId })
  if (typeof rowId !== 'string' || rowId.trim() === '')
    throw new Error('The provider supplied no native tool row ID.')
  if (rowId.includes('\0'))
    throw new Error('The native tool row ID cannot contain NUL.')
  return rowId
}
