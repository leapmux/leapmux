import { randomUUID } from 'node:crypto'
import { expect } from '@playwright/test'
import { BackgroundTaskKind, BackgroundTaskStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { pickObject } from '../../../src/lib/jsonPick'
import { assertPrivateNativePath } from '../helpers/nativeCredentialIsolation'
import { readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { runNativeToolTurn, waitForNativeToolSteps } from '../helpers/nativeToolExecution'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { proveNativeOutputReceipt } from '../helpers/nativeToolOutputFilePaths'
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
  // The proof requires the declared path inside the storage directory, and the storage directory must stay inside the
  // private run directory, so the path stays inside both.
  assertPrivateNativePath(storageRoot, getGlobalState().tmpDir)
  await proveNativeOutputReceipt(
    { context: native, agent, snapshot: await readNativeMessageSnapshot(native, agent.id), nativeCallId: callId, output },
    testInfo,
    (snapshot, id) => readZcodeNativeOutput(snapshot, id, 'GetWorkflowRun'),
    {
      privateRoot: storageRoot,
      extraProof: receipt => expect(pickObject(pickObject(receipt.native, 'data'), 'state')?.input).toMatchObject({ run_id: launch.runId }),
    },
  )
})
