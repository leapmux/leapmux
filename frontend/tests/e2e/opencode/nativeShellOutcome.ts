import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import type { ManagedNativeScenarioContext, NativeToolOutcome } from '../helpers/nativeScenario'
import { acpSupplementProtocol, acpSupplementRawOutput, acpToolSupplement } from '../../../src/components/chat/providers/acp/toolSupplement'
import { ACP_UPDATE } from '../../../src/generated/contracts/acp-protocol'
import { isObject } from '../../../src/lib/jsonPick'
import { nativeMessageBody, nativeMessageSupplement, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'

/** Read the numeric shell outcome from the exact OpenCode-family native result frame. */
export function openCodeShellOutcome(snapshot: NativeMessageSnapshot, callId: string, modelText: string): NativeToolOutcome {
  if (!callId || snapshot.agentId.trim() === '' || snapshot.agentSessionId.trim() === '')
    throw new Error('The native shell result requires an exact agent, session, and call ID.')
  const outcomes: NativeToolOutcome[] = []
  for (const message of snapshot.messages) {
    if (message.agentSessionId !== snapshot.agentSessionId || message.spanId !== callId)
      continue
    const original = nativeMessageBody(message)
    if (!isObject(original))
      throw new Error('The paired native shell result must contain an object.')
    if (original.sessionUpdate !== ACP_UPDATE.ToolCallUpdate)
      continue
    if (original.toolCallId !== callId)
      throw new Error('The paired native shell result identifies another call.')
    if (original.status !== 'completed' && original.status !== 'failed')
      continue
    const decoded = nativeMessageSupplement(message)
    const supplemental = isObject(decoded) ? decoded.provider : undefined
    const extra = acpToolSupplement(original, supplemental)
    const resolved = { ...acpSupplementProtocol(extra), ...original }
    const raw = isObject(resolved.rawOutput) ? resolved.rawOutput : acpSupplementRawOutput(extra)
    if (!isObject(raw) || !isObject(raw.metadata) || !Number.isSafeInteger(raw.metadata.exit))
      throw new Error('The paired native shell result contains no safe integer exit code.')
    const exitCode = raw.metadata.exit
    if (typeof exitCode !== 'number')
      throw new Error('The paired native shell exit code must be a number.')
    const output = typeof raw.output === 'string' ? raw.output : typeof raw.error === 'string' ? raw.error : undefined
    if (output === undefined || output !== modelText)
      throw new Error('The native shell frame and model result contain different output bytes.')
    outcomes.push({ text: modelText, exitCode, failed: original.status === 'failed' || exitCode !== 0 })
  }
  if (outcomes.length !== 1)
    throw new Error('The exact native shell call must have one completed result frame.')
  const outcome = outcomes[0]
  if (!outcome)
    throw new Error('The exact native shell result is absent.')
  return outcome
}

/** Keep model output separate from the Worker frame that proves its native exit status. */
export async function readOpenCodeShellOutcome(context: ManagedNativeScenarioContext, request: MockModelRequestRecord, callId: string): Promise<NativeToolOutcome> {
  const agent = await currentNativeAgent(context)
  const snapshot = await readNativeMessageSnapshot(context, agent.id)
  const text = nativeToolResult(request, callId)
  return openCodeShellOutcome(snapshot, callId, text)
}
