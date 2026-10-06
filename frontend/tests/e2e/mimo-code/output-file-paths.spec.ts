import type { TestInfo } from '@playwright/test'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { Buffer } from 'node:buffer'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { isObject, pickObject } from '../../../src/lib/jsonPick'
import { withCleanup } from '../helpers/cleanup'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { nativeAgentById } from '../helpers/nativeScenario'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { presentPreviewMarkers, proveNativeOutputReceipt } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput } from '../helpers/nativeToolOutputScenario'
import { bashToolCall } from '../helpers/providerToolCalls'
import { openWorkspace } from '../helpers/ui'
import { newProviderWorkingDir, openProviderAgent } from '../helpers/workspace'
import { mimoTest } from '../mimo-fixtures'
import { controlledMiMoOutputFileProducer } from './controlledOutputProducer'
import { readMiMoNativeOutput } from './outputFilePaths'
import { MIMO_AGENT, nativeContext } from './scenarios'

/** Prove native file paths and the original preview for both producer lifecycles. */
async function runMiMoOutputFile(context: ManagedNativeScenarioContext, testInfo: TestInfo, mode: 'quick-exit' | 'observed-size') {
  const { page, leapmuxServer } = context
  const workingDir = newProviderWorkingDir(MIMO_AGENT, 'native-output-path-mimo-')
  const { agentId: openedAgentId } = await openProviderAgent(leapmuxServer, context.workspaceId, MIMO_AGENT, { workingDir })
  await openWorkspace(page, context.workspaceId)
  const output = computedNativeToolOutput({ lineCount: 8000, padding: 30 })
  const nativeHome = leapmuxServer.agentEnv?.MIMOCODE_HOME
  if (!nativeHome)
    throw new Error('The MiMo output path requires the isolated native data directory.')
  const producer = mode === 'observed-size' ? controlledMiMoOutputFileProducer(join(nativeHome, 'data', 'tool-output'), join(workingDir, 'producer-release.txt'), output) : undefined
  await withCleanup(async () => {
    const capturing = captureNativeToolOutput(context, testInfo, {
      output,
      callId: 'native-output-path',
      ...(producer ? { call: () => bashToolCall(context.provider, 'native-output-path', producer.command) } : {}),
      // MiMo keeps its output files in its native data directory.
      proof: capture => proveNativeOutputReceipt(capture, testInfo, readMiMoNativeOutput, {
        privateRoot: nativeHome,
        // MiMo reads the command stream in a fiber of the command scope (packages/cli/src/tool/bash.ts).
        // A quick exit can close that scope before the fiber drains the pipe, so the native preview can end at any line.
        // Every computed line holds the line marker, so the proof does not depend on where the native stream stopped.
        previewMarkers: receipt => [output.lineMarker, ...presentPreviewMarkers(receipt.previewText, output)],
        extraProof: async (receipt) => {
          expect(capture.agent.workingDir).toBe(workingDir)
          const part = pickObject(pickObject(receipt.frame, 'properties'), 'part')
          expect(part?.sessionID).toBe(capture.agent.agentSessionId)
          expect(part?.callID).toBe(capture.nativeCallId)
          expect(part?.tool).toBe('bash')
          const state = pickObject(part, 'state')
          const command = pickObject(state, 'input')?.command
          expect(command).toBe(capture.call.arguments?.command)
          expect(typeof command).toBe('string')
          expect(pickObject(state, 'metadata')?.exit).toBe(0)
          if (producer)
            expect(await producer.observedPath).toBe(receipt.paths[0])
          expect(receipt.previewText.includes(output.lineMarker)).toBe(true)
        },
      }),
    })
    await Promise.all([capturing, ...(producer ? [producer.observedPath] : [])])
  }, async () => {
    try {
      if (producer) {
        const observer = producer.diagnostic()
        let worker: object
        try {
          const agent = await nativeAgentById(context, openedAgentId)
          const snapshot = agent ? await readNativeMessageSnapshot(context, agent.id) : undefined
          const tools = snapshot?.messages.flatMap((message) => {
            const frame = nativeMessageBody(message)
            if (!isObject(frame))
              return []
            const properties = pickObject(frame, 'properties')
            const part = pickObject(properties, 'part')
            if (frame.type !== 'message.part.updated' || part?.type !== 'tool' || part.callID !== 'native-output-path')
              return []
            const state = pickObject(part, 'state')
            const metadata = pickObject(state, 'metadata')
            const output = state?.output
            const progress = metadata?.output
            return [{
              spanId: message.spanId,
              storedSessionId: message.agentSessionId,
              nativeSessionId: part.sessionID,
              nativeCallId: part.callID,
              partId: part.id,
              tool: part.tool,
              status: state?.status,
              command: pickObject(state, 'input')?.command,
              nativeOutputPath: metadata?.outputPath,
              nativeTruncated: metadata?.truncated,
              outputBytes: typeof output === 'string' ? Buffer.byteLength(output) : undefined,
              progressBytes: typeof progress === 'string' ? Buffer.byteLength(progress) : undefined,
            }]
          })
          worker = { agentId: agent?.id, sessionId: agent?.agentSessionId, status: agent?.status, activityState: agent?.activityState, publishedActivityState: agent?.publishedActivityState, activeBackgroundTasks: agent?.activeBackgroundTasks, tools }
        }
        catch (error) {
          worker = { failure: error instanceof Error ? error.message : String(error) }
        }
        await testInfo.attach('mimo-native-file-size-observer', { body: JSON.stringify({ observer, worker }, null, 2), contentType: 'application/json' })
      }
    }
    finally {
      producer?.close()
    }
  })
}

mimoTest('keeps native output paths and the quick-exit preview after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  await runMiMoOutputFile(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId }), testInfo, 'quick-exit')
})

mimoTest('keeps native output paths after native file-size observation', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  await runMiMoOutputFile(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId }), testInfo, 'observed-size')
})
