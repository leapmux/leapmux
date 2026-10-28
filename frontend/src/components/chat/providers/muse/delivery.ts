import { MUSE_LOG_EVENT, MUSE_PARENT_INPUT, MUSE_STREAM_KIND, MUSE_SUPPLEMENT_FIELD } from '~/generated/contracts/muse-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { museNativeRecordSet } from './nativeRecords'

function nativeEvent(record: Record<string, unknown>): Record<string, unknown> | undefined {
  const payload = pickObject(record, 'payload')
  return payload?.kind === MUSE_PARENT_INPUT.Run ? pickObject(payload, 'event') ?? undefined : undefined
}

function nativeControl(record: Record<string, unknown>): Record<string, unknown> | undefined {
  const payload = pickObject(record, 'payload')
  return payload?.kind === MUSE_PARENT_INPUT.SubagentControl ? pickObject(payload, 'record') ?? undefined : undefined
}

function uniqueRecord(records: readonly Record<string, unknown>[], matches: (record: Record<string, unknown>) => boolean): Record<string, unknown> | undefined {
  const found = records.filter(matches)
  return found.length === 1 ? found[0] : undefined
}

function jsonObject(value: unknown): Record<string, unknown> | undefined {
  if (typeof value !== 'string')
    return undefined
  try {
    const parsed: unknown = JSON.parse(value)
    return isObject(parsed) ? parsed : undefined
  }
  catch {
    return undefined
  }
}

function validNativeEnvelope(record: Record<string, unknown>): boolean {
  return record.schema_version === 1 && record.record_type === MUSE_PARENT_INPUT.Event
    && record.durability === MUSE_PARENT_INPUT.Durable && typeof record.payload_schema_version === 'number'
    && Number.isSafeInteger(record.payload_schema_version) && record.payload_schema_version > 0
    && pickString(record, 'payload_type') !== ''
}

/** Read the delivered body only after all six native records prove its owner. */
export function museNativeParentInput(payload: unknown, supplemental: unknown, childSessionId?: string): string | undefined {
  if (!isObject(payload) || !isObject(supplemental) || supplemental[MUSE_SUPPLEMENT_FIELD.Unavailable] !== undefined)
    return undefined
  const parentStream = pickObject(payload, 'stream')
  const parentId = pickString(parentStream, 'id')
  if (parentStream?.kind !== MUSE_STREAM_KIND.Session || !parentId || !validNativeEnvelope(payload))
    return undefined
  const ownSet = museNativeRecordSet([payload], parentId)
  if (!ownSet)
    return undefined
  const event = nativeEvent(payload)
  const snapshot = pickObject(event, 'delivery_snapshot')
  const source = pickObject(snapshot, 'source')
  const target = pickObject(event, 'drain_target_run_stream')
  const inboxId = pickString(event, 'item_id')
  const subagentId = pickString(source, 'subagent_id')
  const commandId = pickString(source, 'command_id')
  if (event?.kind !== MUSE_LOG_EVENT.InboxItemDrained || !inboxId
    || (payload.causation_id !== undefined && payload.causation_id !== null && payload.causation_id !== inboxId)
    || source?.source !== MUSE_PARENT_INPUT.InputSource || source.parent_session_id !== parentId || !subagentId || !commandId
    || target?.kind !== MUSE_PARENT_INPUT.Run || !pickString(target, 'id')
    || (source.input_kind !== MUSE_PARENT_INPUT.SendMessage && source.input_kind !== MUSE_PARENT_INPUT.FollowupTask)
    || typeof snapshot?.body !== 'string') {
    return undefined
  }
  if (source.caller !== undefined) {
    const caller = pickObject(source, 'caller')
    if (!caller || (caller.kind !== MUSE_PARENT_INPUT.ParentAgent && caller.kind !== MUSE_PARENT_INPUT.ChildAgent) || caller.actor_id !== parentId)
      return undefined
  }
  const set = museNativeRecordSet(supplemental[MUSE_SUPPLEMENT_FIELD.NativeRecords], parentId)
  if (!set || set.size !== 5)
    return undefined
  const records = [...set.values()]
  if (records.some(record => !validNativeEnvelope(record)) || set.has(pickString(payload, 'id')))
    return undefined
  const positions = new Set(records.map(record => record.sequence))
  if (positions.has(payload.sequence))
    return undefined
  const spawn = uniqueRecord(records, record => nativeControl(record)?.kind === MUSE_PARENT_INPUT.SpawnAccepted)
  const binding = uniqueRecord(records, record => nativeControl(record)?.kind === MUSE_PARENT_INPUT.ChildSessionBound)
  const attestation = uniqueRecord(records, record => nativeControl(record)?.kind === MUSE_PARENT_INPUT.StartAttested)
  if (!spawn || !binding || !attestation)
    return undefined
  const spawned = nativeControl(spawn)
  const bound = nativeControl(binding)
  const attested = nativeControl(attestation)
  const nativeChildId = pickString(bound, 'child_session_id')
  if (spawned?.subagent_id !== subagentId || spawned.parent_session_id !== parentId
    || bound?.subagent_id !== subagentId || !nativeChildId
    || attested?.subagent_id !== subagentId || attested.subagent_session_id !== nativeChildId
    || attested.run_stream !== `${MUSE_PARENT_INPUT.Run}/${target.id}`
    || (childSessionId !== undefined && childSessionId !== nativeChildId)) {
    return undefined
  }
  const origin = uniqueRecord(records, record => nativeEvent(record)?.kind === MUSE_LOG_EVENT.AssistantToolCallsCommitted)
  const result = uniqueRecord(records, record => nativeEvent(record)?.kind === MUSE_LOG_EVENT.ToolResultBatchCommitted)
  if (!origin || !result)
    return undefined
  const callsEvent = nativeEvent(origin)
  const resultEvent = nativeEvent(result)
  const originRun = pickString(pickObject(origin, 'payload'), 'run_id')
  if (!resultEvent || !originRun || pickString(pickObject(result, 'payload'), 'run_id') !== originRun
    || !pickString(callsEvent, 'message_id') || resultEvent?.batch_id !== callsEvent?.message_id
    || !Array.isArray(callsEvent?.tool_calls) || !Array.isArray(resultEvent.results)) {
    return undefined
  }
  const calls = callsEvent.tool_calls
  if (calls.some(call => !isObject(call) || !pickString(call, 'call_id') || !pickString(call, 'name') || typeof call.args !== 'string'))
    return undefined
  const accepted: Array<{ index: number, callId: string }> = []
  for (const entry of resultEvent.results) {
    if (!isObject(entry) || typeof entry.tool_call_index !== 'number' || !Number.isSafeInteger(entry.tool_call_index)
      || entry.tool_call_index < 0 || typeof entry.tool_call_id !== 'string' || !entry.tool_call_id || typeof entry.text !== 'string') {
      return undefined
    }
    const receipt = jsonObject(entry.text)
    const refs = receipt?.evidence_refs
    if (!Array.isArray(refs) || !refs.includes(inboxId))
      continue
    if (receipt?.status !== MUSE_PARENT_INPUT.Accepted || receipt.subagent_id !== subagentId
      || refs.some(ref => typeof ref !== 'string' || ref === '') || refs.filter(ref => ref === inboxId).length !== 1) {
      return undefined
    }
    accepted.push({ index: entry.tool_call_index, callId: entry.tool_call_id })
  }
  const receipt = accepted[0]
  if (accepted.length !== 1 || !receipt)
    return undefined
  const call = calls[receipt.index]
  if (!isObject(call) || call.call_id !== receipt.callId || call.name !== MUSE_PARENT_INPUT.Tool
    || calls.filter(candidate => isObject(candidate) && candidate.call_id === receipt.callId).length !== 1) {
    return undefined
  }
  const args = jsonObject(call.args)
  if (!args || args.command_id !== commandId || typeof args.message !== 'string'
    || (args.subagent_id !== undefined && args.subagent_id !== subagentId)
    || (args.agent_path !== undefined && (typeof args.agent_path !== 'string' || args.agent_path === '' || args.agent_path !== spawned?.agent_path))
    || (args.artifact_ref !== undefined && typeof args.artifact_ref !== 'string')
    || (args.interrupt !== undefined && typeof args.interrupt !== 'boolean')) {
    return undefined
  }
  if (args.mode !== undefined
    && !((args.mode === MUSE_PARENT_INPUT.Queue && source.input_kind === MUSE_PARENT_INPUT.SendMessage)
      || (args.mode === MUSE_PARENT_INPUT.Followup && source.input_kind === MUSE_PARENT_INPUT.FollowupTask))) {
    return undefined
  }
  return snapshot.body
}
