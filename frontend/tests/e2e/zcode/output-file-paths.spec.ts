import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { randomUUID } from 'node:crypto'
import { expect } from '@playwright/test'
import { BackgroundTaskKind, BackgroundTaskStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { pickObject } from '../../../src/lib/jsonPick'
import { assertPrivateNativePath } from '../helpers/nativeCredentialIsolation'
import { readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { runNativeToolTurn, waitForNativeToolSteps } from '../helpers/nativeToolExecution'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { checkNativeOutputReceipt, expectUnchangedNativeRecord, proveNativeToolOutputFilePaths } from '../helpers/nativeToolOutputFilePaths'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { zcodeCreateWorkflowToolCall, zcodeGetWorkflowRunToolCall, zcodeWorkflowSkillToolCall } from '../helpers/providerToolCalls'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { getGlobalState } from '../helpers/server'
import { sendMessage } from '../helpers/ui'
import { zcodeTest } from '../zcode-fixtures'
import { zcodeWorkflowLaunch } from './codeExecution'
import { readZcodeNativeOutput } from './outputFilePaths'

zcodeTest('keeps a native workflow output path and exact inline preview after reload', async ({ native, leapmuxServer }, testInfo) => {
  const agent = await currentNativeAgent(native)
  const storageRoot = leapmuxServer.agentEnv.ZCODE_STORAGE_DIR
  if (!storageRoot)
    throw new Error('The native ZCode output path requires its private storage directory.')
  const output = computedNativeToolOutput({ lineCount: 6000, padding: 48 })
  const source = `phase("Create a large local result");\n${output.source}\nreturn completeOutput;`
  expect(source).not.toContain(output.omittedMarker)
  const launchId = `native-output-path-launch-${randomUUID()}`
  await native.modelScript.fallback({ text: 'The native output file workflow notification arrived.' })
  const launchStart = await native.modelScript.queue(
    { toolCalls: [zcodeWorkflowSkillToolCall('full-output-workflow-skill')] },
    { toolCalls: [zcodeCreateWorkflowToolCall(launchId, 'native-output-path', source)] },
    { text: 'The native output file workflow launched.' },
  )
  await sendMessage(native.page, native.modelScript.prompt('Run the native large-output workflow once.'))
  await waitForNativeToolSteps(native, launchStart + 3)
  const launch = zcodeWorkflowLaunch(nativeToolResult(await native.modelScript.requestAt(launchStart + 2), launchId))
  await retryUntilPass(async () => {
    const tasks = (await readNativeSidebarSnapshot(native, agent.id)).backgroundTasks.filter(task => task.id === launchId && task.kind === BackgroundTaskKind.WORKFLOW)
    expect(tasks.map(task => task.status), 'the Worker holds one task row of the native ZCode output path workflow, and the row completed')
      .toEqual([BackgroundTaskStatus.COMPLETED])
  })
  const callId = `native-output-path-read-${randomUUID()}`
  const { resultRequest } = await runNativeToolTurn(native, {
    toolCalls: [zcodeGetWorkflowRunToolCall(callId, launch.runId)],
    prompt: 'Read the exact completed workflow once.',
    answer: 'The native output file read returned.',
  })
  const excerpt = nativeToolResult(resultRequest, callId)
  expect(excerpt).not.toContain(output.omittedMarker)
  const readReceipt = (snapshot: NativeMessageSnapshot) => readZcodeNativeOutput(snapshot, callId, 'GetWorkflowRun')
  const receipt = readReceipt(await readNativeMessageSnapshot(native, agent.id))
  await testInfo.attach('zcode-native-output-path-receipt', { body: JSON.stringify({ agentId: agent.id, sessionId: agent.agentSessionId, callId, runId: launch.runId, paths: receipt.paths, previewText: receipt.previewText, frame: receipt.frame, supplement: receipt.supplement }), contentType: 'application/json' })
  const { path, previewMarkers, absentMarker } = checkNativeOutputReceipt(receipt, output)
  assertPrivateNativePath(path, getGlobalState().tmpDir)
  assertPrivateNativePath(path, storageRoot)
  expect(pickObject(pickObject(receipt.native, 'data'), 'state')?.input).toMatchObject({ run_id: launch.runId })
  await proveNativeToolOutputFilePaths({
    context: native,
    callId,
    previewText: receipt.previewText,
    previewMarkers,
    absentMarkers: [absentMarker],
    paths: receipt.paths,
    status: 'completed',
    prepareView: expandNativeResultView,
    workerProof: () => expectUnchangedNativeRecord(native, agent, readReceipt, receipt),
  })
})
