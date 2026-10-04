import { basename, isAbsolute, normalize } from 'node:path'
import { COPILOT_EVENT, COPILOT_METHOD } from '../../../src/generated/contracts/copilot-protocol'
import { isObject } from '../../../src/lib/jsonPick'

export interface CopilotNativeOutputPaths {
  path: string
  excerpt: string
  preview: string | undefined
  retained: string
  shellId: string
}

/** Read the native creator PID from the runtime's output filename. */
export function copilotOutputFileCreatorPid(path: string): number {
  const match = /^\d+-copilot-tool-output-([1-9]\d*)-[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}\.txt$/u.exec(basename(path))
  const pid = Number(match?.[1])
  if (!Number.isSafeInteger(pid) || pid <= 0)
    throw new Error('The native Copilot native output filename has no exact creator PID.')
  return pid
}

/** Read one native shell-exit file from the exact completed session event. */
export function copilotNativeOutputPaths(frames: readonly unknown[], callId: string, sessionId: string): CopilotNativeOutputPaths {
  if (!callId || !sessionId)
    throw new Error('The native Copilot native output requires its exact call and session IDs.')
  const results = frames.filter(isObject).filter(frame => frame.method === COPILOT_METHOD.SessionEvent
    && isObject(frame.params) && frame.params.sessionId === sessionId
    && isObject(frame.params.event) && frame.params.event.type === COPILOT_EVENT.ToolCompleted
    && isObject(frame.params.event.data) && frame.params.event.data.toolCallId === callId)
  const frame = results.length === 1 ? results[0] : undefined
  const params = isObject(frame?.params) ? frame.params : undefined
  const event = isObject(params?.event) ? params.event : undefined
  const data = isObject(event?.data) ? event.data : undefined
  const result = isObject(data?.result) ? data.result : undefined
  if ((event?.agentId !== undefined && event.agentId !== '') || data?.success !== true || typeof result?.content !== 'string' || !Array.isArray(result.contents))
    throw new Error('The native Copilot native output requires one successful complete shell event.')
  const exits = result.contents.filter(isObject).filter(block => block.type === 'shell_exit')
  const exit = exits.length === 1 ? exits[0] : undefined
  if (exit?.exitCode !== 0 || typeof exit.shellId !== 'string' || !exit.shellId
    || (exit.outputTruncated !== undefined && typeof exit.outputTruncated !== 'boolean')
    || (exit.outputPreview !== undefined && typeof exit.outputPreview !== 'string')
    || typeof exit.outputFilePath !== 'string' || !isAbsolute(exit.outputFilePath)
    || normalize(exit.outputFilePath) !== exit.outputFilePath || exit.outputFilePath.includes('\0')) {
    throw new Error('The native Copilot shell event has no unique complete output-file reference.')
  }
  if (result.detailedContent !== undefined && typeof result.detailedContent !== 'string')
    throw new Error('The native Copilot detailed result is not text.')
  return { path: exit.outputFilePath, excerpt: result.content, preview: exit.outputPreview, retained: result.detailedContent ?? result.content, shellId: exit.shellId }
}

/** Keep the original native shell text and remove only its documented exit trailer. */
export function copilotNativePreview(receipt: CopilotNativeOutputPaths): string {
  const trailer = /(?:\r?\n)?<shellId: [^\r\n>]+ completed with exit code -?\d+>\s*$/.exec(receipt.retained)
  return trailer ? receipt.retained.slice(0, trailer.index) : receipt.retained
}
