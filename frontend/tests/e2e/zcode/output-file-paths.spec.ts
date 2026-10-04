import { randomUUID } from 'node:crypto'
import { expect } from '@playwright/test'
import { AgentProvider, BackgroundTaskKind, BackgroundTaskStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { pickObject } from '../../../src/lib/jsonPick'
import { assertPrivateNativePath } from '../helpers/nativeCredentialIsolation'
import { readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { waitForNativeToolSteps } from '../helpers/nativeToolExecution'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { proveNativeToolOutputFilePaths } from '../helpers/nativeToolOutputFilePaths'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { zcodeCreateWorkflowToolCall, zcodeGetWorkflowRunToolCall, zcodeWorkflowSkillToolCall } from '../helpers/providerToolCalls'
import { getGlobalState } from '../helpers/server'
import { sendMessage } from '../helpers/ui'
import { zcodeTest } from '../zcode-fixtures'
import { zcodeWorkflowLaunch } from './codeExecution'
import { readZcodeNativeOutput } from './outputFilePaths'

zcodeTest('keeps a native workflow output path and exact inline preview after reload', async ({ authenticatedZCodeWorkspace, page, context, modelScript, leapmuxServer }, testInfo) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const native = { page, modelScript, leapmuxServer, workspaceId: authenticatedZCodeWorkspace.workspaceId, provider: AgentProvider.ZCODE }
  const agent = await currentNativeAgent(native)
  const storageRoot = leapmuxServer.agentEnv.ZCODE_STORAGE_DIR
  if (!storageRoot)
    throw new Error('The native ZCode output path requires its private storage directory.')
  const output = computedNativeToolOutput({ lineCount: 6000, padding: 48 })
  const source = `phase("Create a large local result");\n${output.source}\nreturn completeOutput;`
  expect(source).not.toContain(output.omittedMarker)
  const launchId = `native-output-path-launch-${randomUUID()}`
  await modelScript.fallback({ text: 'The native output file workflow notification arrived.' })
  await modelScript.queue(
    { toolCalls: [zcodeWorkflowSkillToolCall('full-output-workflow-skill')] },
    { toolCalls: [zcodeCreateWorkflowToolCall(launchId, 'native-output-path', source)] },
    { text: 'The native output file workflow launched.' },
  )
  await sendMessage(page, modelScript.prompt('Run the native large-output workflow once.'))
  await waitForNativeToolSteps(native, 3)
  const launchedRequest = (await modelScript.waitForSteps(3)).requests.find(record => record.stepIndex === 2)
  const launch = zcodeWorkflowLaunch(nativeToolResult(launchedRequest, launchId))
  await expect.poll(async () => {
    const tasks = (await readNativeSidebarSnapshot(native, agent.id)).backgroundTasks.filter(task => task.id === launchId && task.kind === BackgroundTaskKind.WORKFLOW)
    if (tasks.length > 1)
      throw new Error('The native ZCode output path workflow has repeated task rows.')
    return tasks[0]?.status
  }).toBe(BackgroundTaskStatus.COMPLETED)
  const start = (await modelScript.status()).stepCount
  const callId = `native-output-path-read-${randomUUID()}`
  await modelScript.queue({ toolCalls: [zcodeGetWorkflowRunToolCall(callId, launch.runId)] }, { text: 'The native output file read returned.' })
  await sendMessage(page, modelScript.prompt('Read the exact completed workflow once.'))
  await waitForNativeToolSteps(native, start + 2)
  const resultRequest = (await modelScript.waitForSteps(start + 2)).requests.find(record => record.stepIndex === start + 1)
  const excerpt = nativeToolResult(resultRequest, callId)
  expect(excerpt).not.toContain(output.omittedMarker)
  const snapshot = await readNativeMessageSnapshot(native, agent.id)
  const receipt = readZcodeNativeOutput(snapshot, callId, 'GetWorkflowRun')
  const path = receipt.paths[0]
  if (!path)
    throw new Error('The native ZCode workflow result requires its declared path.')
  assertPrivateNativePath(path, getGlobalState().tmpDir)
  assertPrivateNativePath(path, storageRoot)
  expect(pickObject(pickObject(receipt.native, 'data'), 'state')?.input).toMatchObject({ run_id: launch.runId })
  expect(receipt.previewText).not.toContain(output.omittedMarker)
  const previewMarkers = [output.firstMarker, output.lastMarker].filter(marker => receipt.previewText.includes(marker))
  expect(previewMarkers.length).toBeGreaterThan(0)
  await testInfo.attach('zcode-native-output-path-receipt', { body: JSON.stringify({ agentId: agent.id, sessionId: agent.agentSessionId, callId, runId: launch.runId, paths: receipt.paths, previewText: receipt.previewText, frame: receipt.frame, supplement: receipt.supplement }), contentType: 'application/json' })
  await proveNativeToolOutputFilePaths({
    context: native,
    callId,
    previewText: receipt.previewText,
    previewMarkers,
    paths: receipt.paths,
    status: 'completed',
    prepareView: expandNativeResultView,
    workerProof: async () => {
      const current = await currentNativeAgent(native)
      expect(current.id).toBe(agent.id)
      expect(current.agentSessionId).toBe(agent.agentSessionId)
      const captured = await readNativeMessageSnapshot(native, agent.id)
      const retained = readZcodeNativeOutput(captured, callId, 'GetWorkflowRun')
      expect(retained.frame).toEqual(receipt.frame)
      expect(retained.supplement).toEqual(receipt.supplement)
      expect(retained.content).toEqual(receipt.content)
      expect(retained.paths).toEqual(receipt.paths)
      expect(retained.previewText).toBe(receipt.previewText)
    },
  })
})
