import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import type { ManagedNativeScenarioContext, NativeContextFixtures, NativeToolRowIdQuery } from '../helpers/nativeScenario'
import type { ProviderAgent } from '../helpers/workspace'
import { museItem, museParams } from '../../../src/components/chat/providers/muse/protocol'
import { MUSE_ITEM_KIND, MUSE_STREAM_KIND } from '../../../src/generated/contracts/muse-protocol'
import { AgentProvider, MessageSource } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { pickObject, pickString } from '../../../src/lib/jsonPick'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { managedNativeContext } from '../helpers/nativeScenario'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { gitRepositoryWorkingDir } from '../helpers/providerWorkingDir'

/** Muse reads configuration inside its own git repository. */
export const MUSE_AGENT: ProviderAgent = {
  provider: AgentProvider.MUSE_CODE,
  prefix: 'muse-code-e2e',
  workingDir: gitRepositoryWorkingDir,
}

export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return managedNativeContext(fixtures, MUSE_AGENT, {
    resolveToolRowId: async query => museToolRowId(await readNativeMessageSnapshot(fixtures, query.agentId), query),
  })
}

/** Resolve the model call through its exact native session and item lifecycle. */
export function museToolRowId(snapshot: NativeMessageSnapshot, query: NativeToolRowIdQuery): string {
  if (!query.agentId.trim() || !query.callId.trim() || snapshot.agentId !== query.agentId || !snapshot.agentSessionId.trim())
    throw new Error('The native Muse row query requires its exact agent and native session.')
  const latest = new Map<string, { revision: number, item: Record<string, unknown>, params: Record<string, unknown>, spanId: string }>()
  for (const message of snapshot.messages) {
    if (message.agentProvider !== AgentProvider.MUSE_CODE || message.source !== MessageSource.AGENT || message.agentSessionId !== snapshot.agentSessionId)
      continue
    const frame = nativeMessageBody(message)
    const item = museItem(frame)
    const params = museParams(frame)
    if (!item || !params || item.kind !== MUSE_ITEM_KIND.ToolCall || params.sessionId !== snapshot.agentSessionId)
      continue
    const id = pickString(item, 'itemId')
    if (!id.trim() || !Number.isSafeInteger(item.revision) || typeof item.revision !== 'number' || item.revision < 1)
      throw new Error('The native Muse row supplies an invalid item lifecycle.')
    const previous = latest.get(id)
    if (!previous || item.revision > previous.revision)
      latest.set(id, { revision: item.revision, item, params, spanId: message.spanId })
  }
  const candidates = [...latest.values()].filter(candidate => candidate.item.callId === query.callId)
  if (candidates.length !== 1 || !candidates[0])
    throw new Error('The native Muse call requires exactly one item in its owned session.')
  const { item, params, spanId } = candidates[0]
  const range = pickObject(params, 'sourceRange')
  const stream = pickObject(range, 'stream')
  const first = pickObject(range, 'first')
  if (spanId !== item.itemId || !pickString(item, 'turnId').trim() || !pickString(item, 'tool').trim() || typeof item.args !== 'string'
    || stream?.kind !== MUSE_STREAM_KIND.Session || stream.id !== snapshot.agentSessionId || !pickString(first, 'id').trim()
    || typeof first?.sequence !== 'number' || !Number.isSafeInteger(first.sequence) || first.sequence < 1) {
    throw new Error('The native Muse row supplies no exact span and origin identity.')
  }
  return pickString(item, 'itemId')
}

export function nativeLaunch(context: ManagedNativeScenarioContext) {
  return resolveNativeStartupLaunch(context.leapmuxServer.agentEnv, { binaryName: 'muse', holdWhen: ['serve'] })
}
