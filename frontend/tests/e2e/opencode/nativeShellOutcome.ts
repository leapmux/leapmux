import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import type { ManagedNativeScenarioContext, NativeToolOutcome } from '../helpers/nativeScenario'
import { OPENCODE_NATIVE_TOOL_STATUS } from '../../../src/generated/contracts/opencode-native-protocol'
import { isObject, pickObject } from '../../../src/lib/jsonPick'
import { readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { openCodeNativeToolRecords } from './nativeToolRecords'

/** Compare the native numeric exit status and original content with the model receipt. */
export function openCodeShellOutcome(snapshot: NativeMessageSnapshot, callId: string, modelText: string): NativeToolOutcome {
  const endings = openCodeNativeToolRecords(snapshot, callId).filter(record => record.finished)
  if (endings.length !== 1 || !endings[0])
    throw new Error('The exact native shell call must have one completed result frame.')
  const { data, status } = endings[0]
  const exitCode = pickObject(data, 'metadata')?.exit
  if (typeof exitCode !== 'number' || !Number.isSafeInteger(exitCode))
    throw new TypeError('The paired native shell result contains no safe integer exit code.')
  const output = Array.isArray(data.content)
    ? data.content.flatMap(item => isObject(item) && item.type === 'text' && typeof item.text === 'string'
        ? [item.text]
        : []).join('\n')
    : undefined
  if (output === undefined || output !== modelText)
    throw new Error('The native shell frame and model result contain different output bytes.')
  return { text: modelText, exitCode, failed: status === OPENCODE_NATIVE_TOOL_STATUS.Error || exitCode !== 0 }
}

/** Read only stored native rows and the model receipt; never infer status from prose. */
export async function readOpenCodeShellOutcome(context: ManagedNativeScenarioContext, request: MockModelRequestRecord, callId: string): Promise<NativeToolOutcome> {
  const agent = await currentNativeAgent(context)
  return openCodeShellOutcome(await readNativeMessageSnapshot(context, agent.id), callId, nativeToolResult(request, callId))
}
