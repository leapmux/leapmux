import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { PI_EVENT, PI_TOOL } from '../../../src/generated/contracts/pi-protocol'
import { prettifyJson } from '../../../src/lib/jsonFormat'
import { isObject, pickObject } from '../../../src/lib/jsonPick'
import { isFilesystemPath } from '../../../src/lib/paths'
import { readNativeMessageSnapshot, readNativeToolOutputRecord } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { expectUnchangedNativeRecord, proveNativeToolOutputFilePaths } from '../helpers/nativeToolOutputFilePaths'

export interface PiOutputPathProof {
  callId: string
  toolName: string
  agentId: string
  agentSessionId: string
}

/** Read the native codemode result, its output path, and its original Copy text. */
export function readPiNativeOutput(snapshot: NativeMessageSnapshot, proof: PiOutputPathProof) {
  if (!proof.callId || proof.toolName !== PI_TOOL.Codemode || !proof.agentId || !proof.agentSessionId
    || snapshot.agentId !== proof.agentId || snapshot.agentSessionId !== proof.agentSessionId) {
    throw new Error('The Pi output path proof requires its exact native session and codemode call.')
  }
  const record = readNativeToolOutputRecord(snapshot, {
    callId: proof.callId,
    spanId: proof.callId,
    accepts: frame => frame.type === PI_EVENT.ToolExecutionEnd && frame.toolCallId === proof.callId && frame.toolName === proof.toolName,
  })
  const result = pickObject(record.frame, 'result')
  const details = pickObject(result, 'details')
  const path = details?.fullOutputPath
  if (record.message.spanType !== proof.toolName || record.frame.isError !== false || !isFilesystemPath(path)
    || !/[\\/]pi-codemode-[0-9a-f]{16}\.txt$/u.test(path) || !Array.isArray(result?.content)) {
    throw new Error('The native Pi result has no exact successful span and filesystem pointer.')
  }
  const blocks = result.content
  if (blocks.some(block => !isObject(block) || block.type !== 'text' || typeof block.text !== 'string'))
    throw new Error('The Pi text-only path proof contains another native content kind.')
  const texts = blocks.filter(isObject).map(block => String(block.text))
  const header = texts[0]
  const excerpt = texts[1]
  if (!header || !/^Script completed\nWall time \d+(?:\.\d+)? seconds\nOutput:\n$/u.test(header) || excerpt === undefined)
    throw new Error('The native Pi result has no successful codemode header and preview.')
  const previewText = [texts.filter(Boolean).join('\n\n'), prettifyJson(JSON.stringify(details))].filter(Boolean).join('\n\n')
  return { nativeResult: result, paths: [path], previewText, excerpt, frame: record.frame, content: record.message.content }
}

/**
 * Prove the native preview and path. Read no output file.
 *
 * The preview joins the codemode `details`, which list each nested call with its arguments. A nested call can take
 * the complete output as an argument, as the MCP case does, so the preview can hold the middle line. The proof thus
 * states no absent line, and only the excerpt of the codemode output must omit the middle line.
 *
 * `proveNativeOutputReceipt` cannot take this proof. Its receipt check (`checkNativeOutputReceipt`) refuses a preview
 * that holds the absent line, and `argumentsHoldOutput` relaxes only the check of the result row.
 */
export async function verifyPiOutputFilePaths(context: ManagedNativeScenarioContext, input: { callId: string, expectedText: string, omittedMarker: string }) {
  const agent = await currentNativeAgent(context)
  const proof = { callId: input.callId, toolName: PI_TOOL.Codemode, agentId: agent.id, agentSessionId: agent.agentSessionId }
  const readReceipt = (snapshot: NativeMessageSnapshot) => readPiNativeOutput(snapshot, proof)
  const receipt = readReceipt(await readNativeMessageSnapshot(context, agent.id))
  expect(receipt.excerpt).not.toContain(input.omittedMarker)
  const first = input.expectedText.split('\n', 1)[0]
  const last = input.expectedText.slice(input.expectedText.lastIndexOf('\n') + 1)
  if (!first || !last)
    throw new Error('The native Pi producer requires distinct output markers.')
  await proveNativeToolOutputFilePaths({
    context,
    callId: input.callId,
    previewText: receipt.previewText,
    previewMarkers: [first, last],
    paths: receipt.paths,
    status: 'completed',
    prepareView: expandNativeResultView,
    workerProof: () => expectUnchangedNativeRecord(context, agent, readReceipt, receipt),
  })
}
