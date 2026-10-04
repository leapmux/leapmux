import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import type { JunieNativeOutputPaths } from './outputFilePaths'
import { ACP_UPDATE } from '../../../src/generated/contracts/acp-protocol'
import { JUNIE_SUPPLEMENT } from '../../../src/generated/contracts/junie-protocol'
import { MESSAGE_SUPPLEMENT_FIELD } from '../../../src/generated/contracts/worker-vocab'
import { isObject, pickObject } from '../../../src/lib/jsonPick'
import { isFilesystemPath } from '../../../src/lib/paths'
import { nativeMessageBody, nativeMessageSupplement } from '../helpers/nativeMessages'
import { readJunieNativeOutputPaths } from './outputFilePaths'

export interface JunieOutputPathOwner {
  agentId: string
  sessionId: string
  callId: string
  command: string
  workingDirectory: string
}

export interface JunieOutputPathObservation {
  readSnapshot: () => Promise<NativeMessageSnapshot>
  waitUntilSettled: (observe: () => Promise<boolean>) => Promise<void>
}

export interface JunieReadyOutputPaths {
  snapshot: NativeMessageSnapshot
  receipt: JunieNativeOutputPaths
}

function validateOwner(owner: JunieOutputPathOwner): void {
  if (!owner.agentId.trim() || !owner.sessionId.trim() || !owner.callId.trim() || !owner.command.trim()
    || !isFilesystemPath(owner.workingDirectory)) {
    throw new Error('The Junie output path observation requires its exact native owner and command.')
  }
}

/** Check completion and pointer presence. The strict reader validates the receipt afterward. */
export function junieOutputFilePathReady(snapshot: NativeMessageSnapshot, owner: JunieOutputPathOwner): boolean {
  validateOwner(owner)
  if (snapshot.agentId !== owner.agentId || snapshot.agentSessionId !== owner.sessionId)
    throw new Error('The Junie output path observation belongs to another agent or native session.')
  const completed = snapshot.messages.filter((message) => {
    if (message.spanId !== owner.callId)
      return false
    if (message.agentSessionId !== owner.sessionId || message.spanType !== 'execute')
      throw new Error('The Junie output path row belongs to another native owner.')
    const frame = nativeMessageBody(message)
    if (!isObject(frame) || frame.toolCallId !== owner.callId)
      throw new Error('The Junie output path row has a different native call identity.')
    if (frame.status === 'failed')
      throw new Error('The Junie native command failed before its output path was ready.')
    if (frame.sessionUpdate !== ACP_UPDATE.ToolCallUpdate || frame.status !== 'completed')
      return false
    if (frame.kind !== 'execute')
      throw new Error('The Junie completed row has a different native tool kind.')
    return true
  })
  if (completed.length > 1)
    throw new Error('The Junie output path observation contains duplicate completed rows.')
  const message = completed[0]
  if (!message)
    return false
  const supplement = nativeMessageSupplement(message)
  if (supplement === undefined)
    return false
  if (!isObject(supplement))
    throw new Error('The Junie completed row has a malformed supplement.')
  if (!Object.hasOwn(supplement, MESSAGE_SUPPLEMENT_FIELD.Provider))
    return false
  const provider = supplement[MESSAGE_SUPPLEMENT_FIELD.Provider]
  if (!isObject(provider))
    throw new Error('The Junie completed row has a malformed provider supplement.')
  // A present invalid value must reach strict validation once, instead of keeping the wait active.
  return Object.hasOwn(provider, JUNIE_SUPPLEMENT.OutputFilePath)
}

type ObservationState
  = | { kind: 'pending' }
    | { kind: 'ready', snapshot: NativeMessageSnapshot }
    | { kind: 'failed', cause: unknown }

/** Retain the fresh ready snapshot and decode it once without retrying failed reads or receipts. */
export async function waitForJunieOutputFilePaths(
  owner: JunieOutputPathOwner,
  operations: JunieOutputPathObservation,
): Promise<JunieReadyOutputPaths> {
  const expected = { ...owner }
  validateOwner(expected)
  const observation: { state: ObservationState } = { state: { kind: 'pending' } }
  await operations.waitUntilSettled(async () => {
    if (observation.state.kind !== 'pending')
      return true
    try {
      const snapshot = await operations.readSnapshot()
      if (junieOutputFilePathReady(snapshot, expected))
        observation.state = { kind: 'ready', snapshot }
    }
    catch (cause) {
      observation.state = { kind: 'failed', cause }
    }
    return observation.state.kind !== 'pending'
  })
  const state = observation.state
  if (state.kind === 'failed')
    throw state.cause
  if (state.kind === 'pending')
    throw new Error('The Junie output path wait ended before its Worker row was ready.')
  const receipt = readJunieNativeOutputPaths(state.snapshot, expected.callId)
  const input = pickObject(receipt.frame, 'rawInput')
  if (!input || input.command !== expected.command || input.cwd !== expected.workingDirectory)
    throw new Error('The Junie output path result differs from the expected native command or working directory.')
  return { snapshot: state.snapshot, receipt }
}
