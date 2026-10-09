import type { AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import type { ManagedNativeScenarioContext, NativeToolRowIdResolver } from '../helpers/nativeScenario'
import { mimoActorSpawns } from '../../../src/components/chat/providers/mimo/extractors/agent'
import { mimoEvent, mimoPart, mimoToolPart } from '../../../src/components/chat/providers/mimo/extractors/toolCommon'
import { MIMO_PART_TYPE, MIMO_TOOL, MIMO_TOOL_STATUS } from '../../../src/generated/contracts/mimo-protocol'
import { AgentProvider, AgentStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeMessageBody, readNativeMessageSnapshot, sameAgentOwnership } from '../helpers/nativeMessages'
import { nativeAgentById } from '../helpers/nativeScenario'

interface MiMoToolRowOwner {
  agentId: string
  nativeSessionId: string
}

const TOOL_STATUSES: ReadonlySet<string> = new Set(Object.values(MIMO_TOOL_STATUS))

/** An optional outer event session must agree with the exact session of the queried native part. */
function requireMiMoOuterSession(body: unknown, nativeSessionId: string): void {
  const outerSession = mimoEvent(body)?.properties.sessionID
  if (outerSession !== undefined && outerSession !== nativeSessionId)
    throw new Error('The MiMo native event has conflicting outer session ownership.')
}

/** Read one distinct native part in the exact Worker transcript and native session. */
export function readMiMoNativeToolRowId(snapshot: NativeMessageSnapshot, callId: string, owner: MiMoToolRowOwner): string {
  if (callId.trim() === '' || owner.agentId.trim() === '' || owner.nativeSessionId.trim() === '')
    throw new Error('The MiMo row query requires a call ID and exact native ownership.')
  if (snapshot.agentId !== owner.agentId || snapshot.agentSessionId !== owner.nativeSessionId)
    throw new Error('The MiMo row snapshot belongs to another Worker owner.')
  const parts = new Map<string, string>()
  for (const message of snapshot.messages) {
    if (message.agentSessionId !== owner.nativeSessionId)
      continue
    const body = nativeMessageBody(message)
    const rawPart = mimoPart(body)
    if (!rawPart || rawPart.type !== MIMO_PART_TYPE.Tool || rawPart.callID !== callId || rawPart.sessionID !== owner.nativeSessionId)
      continue
    requireMiMoOuterSession(body, owner.nativeSessionId)
    const part = mimoToolPart(body)
    if (!part)
      throw new Error('The queried native tool part is incomplete.')
    if (part.partId.trim() === '' || part.messageId.trim() === '')
      throw new Error('The MiMo tool row has no native part or message identity.')
    if (message.spanId !== part.partId)
      throw new Error('The Worker span does not match the MiMo native part.')
    if (!TOOL_STATUSES.has(part.status))
      throw new Error('The MiMo native tool state has an unknown status.')
    const previous = parts.get(part.partId)
    if (previous !== undefined && previous !== part.messageId)
      throw new Error('The MiMo tool part belongs to conflicting native messages.')
    parts.set(part.partId, part.messageId)
  }
  const ids = [...parts.keys()]
  if (ids.length !== 1 || ids[0] === undefined)
    throw new Error(`The MiMo model call matches ${ids.length} distinct native tool parts in its Worker owner.`)
  return ids[0]
}

function requireMiMoAgent(agent: AgentInfo | null, id: string): AgentInfo {
  if (!agent || agent.id !== id || agent.agentProvider !== AgentProvider.MIMO_CODE || agent.status !== AgentStatus.ACTIVE)
    throw new Error('The MiMo row query requires its exact active Worker agent.')
  return agent
}

/** Validate the retained parent spawn without requiring metadata that only a later native update supplies. */
export function requireMiMoChildScope(child: AgentInfo, parent: AgentInfo, snapshot: NativeMessageSnapshot): void {
  if (child.parentAgentId !== parent.id || child.rootAgentId !== parent.id || child.agentSessionId.trim() === ''
    || parent.agentSessionId.trim() === ''
    || snapshot.agentId !== parent.id || snapshot.agentSessionId !== parent.agentSessionId) {
    throw new Error('The MiMo child has no exact durable parent and native session owner.')
  }
  const nativeSessionId = child.agentSessionId
  const key = child.providerChildKey
  const fallbackPrefix = `${nativeSessionId}/`
  if (key.startsWith(fallbackPrefix)) {
    const actorId = key.slice(fallbackPrefix.length)
    if (actorId.trim() === '' || actorId === 'main' || child.spawnSpanId !== key)
      throw new Error('The MiMo fallback child key has no exact actor and spawn identity.')
    return
  }
  if (key.trim() === '' || child.spawnSpanId !== key)
    throw new Error('The MiMo child has no exact native spawn part link.')
  let found = false
  let messageId: string | undefined
  let actorId: string | undefined
  for (const message of snapshot.messages) {
    if (message.agentSessionId !== nativeSessionId || message.spanId !== key)
      continue
    const body = nativeMessageBody(message)
    const rawPart = mimoPart(body)
    if (!rawPart || rawPart.id !== key || rawPart.sessionID !== nativeSessionId)
      continue
    requireMiMoOuterSession(body, nativeSessionId)
    const part = mimoToolPart(body)
    if (!part)
      throw new Error('The original native actor spawn is incomplete.')
    if (part.tool !== MIMO_TOOL.Actor || !mimoActorSpawns(part.input) || part.messageId.trim() === '' || !TOOL_STATUSES.has(part.status))
      throw new Error('The MiMo child link does not identify its original native actor spawn.')
    if (messageId !== undefined && messageId !== part.messageId)
      throw new Error('The MiMo child spawn belongs to conflicting native messages.')
    messageId = part.messageId
    // A held run can retain its opening before actorId arrives on a later running update.
    if (Object.hasOwn(part.metadata, 'actorId')) {
      const value = part.metadata.actorId
      if (typeof value !== 'string' || value.trim() === '' || value === 'main' || (actorId !== undefined && actorId !== value))
        throw new Error('The MiMo child spawn has an invalid or conflicting native actor ID.')
      actorId = value
    }
    found = true
  }
  if (!found)
    throw new Error('The MiMo child has no original spawn frame in its captured native session.')
}

/** Resolve from the selected transcript. Only parent ownership evidence can come from the parent transcript. */
export function mimoToolRowIdResolver(context: Pick<ManagedNativeScenarioContext, 'leapmuxServer'>): NativeToolRowIdResolver {
  const worker = { leapmuxServer: context.leapmuxServer }
  return async ({ callId, agentId }) => {
    const before = requireMiMoAgent(await nativeAgentById(worker, agentId), agentId)
    let parent: AgentInfo | undefined
    const nativeSessionId = before.agentSessionId
    if (before.parentAgentId !== '') {
      parent = requireMiMoAgent(await nativeAgentById(worker, before.parentAgentId), before.parentAgentId)
      requireMiMoChildScope(before, parent, await readNativeMessageSnapshot(worker, parent.id))
    }
    if (nativeSessionId.trim() === '')
      throw new Error('The MiMo row owner has no native session.')
    const snapshot = await readNativeMessageSnapshot(worker, agentId)
    const rowId = readMiMoNativeToolRowId(snapshot, callId, { agentId, nativeSessionId })
    const after = requireMiMoAgent(await nativeAgentById(worker, agentId), agentId)
    if (!sameAgentOwnership(before, after))
      throw new Error('The MiMo selected transcript owner changed during row resolution.')
    if (parent) {
      const currentParent = requireMiMoAgent(await nativeAgentById(worker, parent.id), parent.id)
      if (!sameAgentOwnership(parent, currentParent))
        throw new Error('The MiMo parent owner changed during child row resolution.')
    }
    return rowId
  }
}
