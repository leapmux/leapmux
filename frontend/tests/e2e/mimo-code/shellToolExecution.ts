import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import type { ManagedNativeScenarioContext, NativeToolOutcome } from '../helpers/nativeScenario'
import type { ShellToolExecutionOptions } from '../helpers/nativeToolExecution'
import { MIMO_EVENT, MIMO_PART_TYPE, MIMO_TOOL, MIMO_TOOL_STATUS } from '../../../src/generated/contracts/mimo-protocol'
import { isObject, pickObject } from '../../../src/lib/jsonPick'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'

/**
 * Read the exit of one MiMo Code shell call from its stored Worker frame, and the output from the model request.
 *
 * MiMo Code 0.1.15 gives its model the output of a command and no exit code, so no model request can prove the code.
 * It states the code as `state.metadata.exit` of the completed `bash` part, which is the frame that the row reads. The
 * reader requires that the frame states the same output as the model request, so both describe one call.
 */
export function mimoShellOutcome(snapshot: NativeMessageSnapshot, callId: string, modelText: string): NativeToolOutcome {
  if (!callId || snapshot.agentSessionId.trim() === '')
    throw new Error('The native MiMo shell result requires an exact call ID and native session.')
  const outcomes: NativeToolOutcome[] = []
  for (const message of snapshot.messages) {
    if (message.agentSessionId !== snapshot.agentSessionId || message.spanId !== callId)
      continue
    const frame = nativeMessageBody(message)
    const part = isObject(frame) && frame.type === MIMO_EVENT.MessagePartUpdated ? pickObject(pickObject(frame, 'properties'), 'part') : undefined
    const state = pickObject(part, 'state')
    if (part?.type !== MIMO_PART_TYPE.Tool || part.tool !== MIMO_TOOL.Bash || part.callID !== callId || state?.status !== MIMO_TOOL_STATUS.Completed)
      continue
    const metadata = pickObject(state, 'metadata')
    const exitCode = metadata?.exit
    if (typeof exitCode !== 'number' || !Number.isSafeInteger(exitCode))
      throw new Error('The completed native MiMo shell part states no safe integer exit code.')
    if (metadata?.output !== modelText)
      throw new Error('The native MiMo shell part and the model request state different output bytes.')
    outcomes.push({ text: modelText, exitCode, failed: exitCode !== 0 })
  }
  const [outcome, ...others] = outcomes
  if (!outcome || others.length > 0)
    throw new Error('The exact native MiMo shell call must have one completed part.')
  return outcome
}

/** Read the outcome of one MiMo Code shell call of the current agent. */
async function readMiMoShellOutcome(context: ManagedNativeScenarioContext, request: MockModelRequestRecord, callId: string): Promise<NativeToolOutcome> {
  const agent = await currentNativeAgent(context)
  return mimoShellOutcome(await readNativeMessageSnapshot(context, agent.id), callId, nativeToolResult(request, callId))
}

/**
 * Run the shared shell scenario with an output gate on every command, and the MiMo reader of the exit.
 *
 * MiMo Code 0.1.15 reads the output of a command in a fiber, and its bash tool
 * ends that fiber when the command exits (`BashTool.run`). A command that exits
 * right after it writes can lose its output, and the tool then answers
 * "(no output)". The gate holds each command until the live view shows its
 * output. The tool then has the output before the command ends.
 *
 * A MiMo spec that asserts the output of a shell command calls this function.
 * It does not call the shared scenario.
 */
export function exerciseMiMoShellToolExecution(
  context: ManagedNativeScenarioContext,
  options: Omit<ShellToolExecutionOptions, 'outputGate'> = {},
): Promise<void> {
  return exerciseShellToolExecution({ ...context, readToolResult: (request, callId) => readMiMoShellOutcome(context, request, callId) }, { ...options, outputGate: true })
}
