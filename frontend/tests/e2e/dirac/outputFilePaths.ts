import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import type { DiracScriptFrame, DiracScriptReceipt } from './codeExecution'
import { relative, sep } from 'node:path'
import { DIRAC_OUTPUT_REFERENCE } from '../../../src/components/chat/providers/dirac/protocol'
import { DIRAC_TOOL } from '../../../src/generated/contracts/dirac-protocol'
import { isObject } from '../../../src/lib/jsonPick'
import { isFilesystemPath } from '../../../src/lib/paths'
import { acpClosedToolCall } from '../helpers/acpToolFrame'
import { nativeMessageBody, nativeMessageSupplement } from '../helpers/nativeMessages'
import { diracScriptReceipt } from './codeExecution'

export interface DiracNativeOutputPaths extends DiracScriptReceipt {
  paths: string[]
  previewText: string
}

/** Keep original current-session packets before native command interpretation. */
export function diracOutputFileFrames(snapshot: Pick<NativeMessageSnapshot, 'agentSessionId' | 'messages'>): DiracScriptFrame[] {
  if (!snapshot.agentSessionId)
    throw new Error('The native Dirac output requires its current Worker session.')
  return snapshot.messages.filter(message => message.agentSessionId === snapshot.agentSessionId)
    .map(message => ({ original: nativeMessageBody(message), supplemental: nativeMessageSupplement(message) }))
}

/** Read one final native log pointer without reading its file. */
export function diracNativeLogPath(output: unknown): string | undefined {
  if (typeof output !== 'string')
    return undefined
  const lines = output.split('\n')
  const pointers = lines.filter(line => line.startsWith(DIRAC_OUTPUT_REFERENCE.TextPrefix))
  const last = lines.at(-1)
  if (pointers.length !== 1 || pointers[0] !== last)
    return undefined
  const path = last?.slice(DIRAC_OUTPUT_REFERENCE.TextPrefix.length)
  return isFilesystemPath(path) && /[\\/]dirac[\\/]large-output-\d+-[a-z0-9]+\.log$/u.test(path) ? path : undefined
}

/** Require the generated command owner and its declared private log path. */
export function diracNativeOutputPaths(frames: readonly DiracScriptFrame[], script: string, temporaryDir: string): DiracNativeOutputPaths {
  if (!isFilesystemPath(temporaryDir))
    throw new Error('The native Dirac output requires its private runtime temp directory.')
  const receipt = diracScriptReceipt(frames, script)
  const path = diracNativeLogPath(receipt.output)
  const parts = path ? relative(temporaryDir, path).split(sep) : []
  if (!path || parts.length !== 2 || parts[0] !== DIRAC_OUTPUT_REFERENCE.DirectoryName || parts.includes('..'))
    throw new Error('The native Dirac log belongs to another runtime temp directory.')
  const closing = frames.filter(({ original }) => isObject(original) && acpClosedToolCall(original, receipt.callId))
  if (closing.length !== 1 || !isObject(closing[0]?.original) || closing[0].original.name !== DIRAC_TOOL.ExecuteCommand)
    throw new Error('The native Dirac output requires one completed generated command card.')
  return { ...receipt, paths: [path], previewText: receipt.output }
}
