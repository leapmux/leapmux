import type { QoderWorkflowLaunch } from './codeExecution'
import { lstatSync, readFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { AgentProvider, BackgroundTaskKind, BackgroundTaskStatus, ListAgentInputQueueRequestSchema, ListAgentInputQueueResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { getTestChannel, openAgentViaAPI } from '../helpers/api'
import { withCleanup } from '../helpers/cleanup'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { assertPrivateNativePath } from '../helpers/nativeCredentialIsolation'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent, nativeAgentById, nativeModelToolNames } from '../helpers/nativeScenario'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { waitForNativeToolSteps } from '../helpers/nativeToolExecution'
import { qoderWorkflowToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { getGlobalState } from '../helpers/server'
import { uniqueMarker } from '../helpers/shellArguments'
import { expandBackgroundTasksSection } from '../helpers/subagentRegistry'
import { openWorkspace, sendMessage } from '../helpers/ui'
import { expect, qoderTest } from '../qoder-fixtures'
import { qoderWorkflowDiagnosticJson, qoderWorkflowLaunch, qoderWorkflowModelOutcome, qoderWorkflowOutput } from './codeExecution'

qoderTest('executes native Workflow code and preserves the computed result and script error', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.QODER }
  await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, context.workspaceId, createTestDirectory('native-workflow-code-'), { agentProvider: context.provider, ...agentOpenOptions(agentSettings(context.provider)) })
  await openWorkspace(page, context.workspaceId)
  const catalogRequest = await sendNativeAnswer(context, 'Reply once while the native tool catalog remains available.', 'The actual native catalog turn completed.')
  expect(nativeModelToolNames(catalogRequest)).toContain('Workflow')
  await testInfo.attach('qoder-workflow-catalog', { body: JSON.stringify(catalogRequest, null, 2), contentType: 'application/json' })
  const agent = await currentNativeAgent(context)
  const marker = uniqueMarker('NATIVEWORKFLOW')
  // Workflow completion can start another parent turn after the launch turn ends.
  await modelScript.fallback({ text: 'The native Workflow notification arrived.' })
  const runs: { launch: QoderWorkflowLaunch, failed: boolean }[] = []
  for (const failed of [false, true]) {
    const callId = `workflow-code-${failed ? 'error' : 'output'}`
    const name = `native-code-${failed ? 'error' : 'output'}-${marker}`
    const source = [
      `export const meta = { name: ${JSON.stringify(name)}, description: 'Compute one local value.' };`,
      failed ? `throw new Error(${JSON.stringify(marker)} + (70 + 7));` : `return ${JSON.stringify(marker)} + (40 + 2);`,
    ].join('\n')
    const expected = `${marker}${failed ? 77 : 42}`
    expect(source).not.toContain(expected)
    const start = (await modelScript.status()).stepCount
    await modelScript.queue({ toolCalls: [qoderWorkflowToolCall(callId, source)] }, { text: 'The native Workflow launch turn ended.' })
    const evidence: Record<string, unknown> = { callId, source, agentId: agent.id, agentSessionId: agent.agentSessionId }
    await withCleanup(async () => {
      await sendMessage(page, modelScript.prompt(`Run the native Workflow ${failed ? 'error' : 'output'} script.`))
      await waitForNativeToolSteps(context, start + 2, { beforeIdle: async () => {
        const channel = await getTestChannel(leapmuxServer.hubUrl, leapmuxServer.adminToken)
        const [messages, sidebar, current, status, queue] = await Promise.all([
          readNativeMessageSnapshot(context, agent.id),
          readNativeSidebarSnapshot(context, agent.id),
          nativeAgentById(context, agent.id),
          modelScript.status(),
          channel.callWorker(leapmuxServer.workerId, 'ListAgentInputQueue', ListAgentInputQueueRequestSchema, ListAgentInputQueueResponseSchema, { agentId: agent.id }),
        ])
        evidence.beforeIdle = {
          agent: current,
          agentId: messages.agentId,
          agentSessionId: messages.agentSessionId,
          messages: messages.messages.map(message => ({ id: message.id, seq: message.seq.toString(), body: nativeMessageBody(message) })),
          tasks: sidebar.backgroundTasks,
          modelStatus: status,
          inputQueue: queue,
        }
        await testInfo.attach(`qoder-workflow-${failed ? 'error' : 'output'}-before-idle`, {
          body: qoderWorkflowDiagnosticJson(evidence.beforeIdle),
          contentType: 'application/json',
        })
        if (!current || current.id !== agent.id || current.agentSessionId !== agent.agentSessionId)
          throw new Error('The Qoder Workflow changed native agent identity before the idle wait.')
      } })
      const initial = await readNativeMessageSnapshot(context, agent.id)
      evidence.messages = initial.messages.map(nativeMessageBody)
      evidence.tasks = (await readNativeSidebarSnapshot(context, agent.id)).backgroundTasks
      const launches = initial.messages.map(nativeMessageBody).flatMap((value) => {
        const launch = qoderWorkflowLaunch(value, callId)
        return launch ? [launch] : []
      })
      if (launches.length !== 1)
        throw new Error(`The Qoder Workflow has ${launches.length} exact native launch receipts.`)
      const launch = launches[0]!
      evidence.launch = launch
      if (launch.sessionId !== agent.agentSessionId)
        throw new Error('The Qoder Workflow launch changed the native session.')
      const rowKey = `workflow:${launch.sessionId}:${launch.callId}`
      await expect.poll(async () => {
        const tasks = (await readNativeSidebarSnapshot(context, agent.id)).backgroundTasks
        evidence.tasks = tasks
        return Boolean(tasks.find(task => task.id === rowKey)?.endedAt)
      }).toBe(true)
      await expect.poll(async () => {
        const status = await modelScript.status()
        evidence.modelRequests = status.requests
        return qoderWorkflowModelOutcome(status.requests.map(request => request.body), launch) !== undefined
      }).toBe(true)
      const modelStatus = await modelScript.status()
      evidence.modelRequests = modelStatus.requests
      const outcome = qoderWorkflowModelOutcome(modelStatus.requests.map(request => request.body), launch)
      if (!outcome)
        throw new Error('The Qoder Workflow lost its exact final native notification after the wait.')
      evidence.outcome = outcome
      const tasks = (await readNativeSidebarSnapshot(context, agent.id)).backgroundTasks
      const task = tasks.find(value => value.id === rowKey)
      evidence.task = task
      await testInfo.attach(`qoder-workflow-${failed ? 'error' : 'output'}-native-pre-full-output`, { body: qoderWorkflowDiagnosticJson(evidence), contentType: 'application/json' })
      const outputFile = join(agent.workingDir, '.qoder', 'sessions', launch.sessionId, 'workflows', 'runs', launch.runId, 'output.json')
      if (resolve(launch.transcriptDir) !== resolve(join(outputFile, '..')) || outcome.outputFile !== outputFile)
        throw new Error('The Qoder Workflow output path does not match the exact native run.')
      assertPrivateNativePath(outputFile, getGlobalState().tmpDir)
      const stat = lstatSync(outputFile)
      if (!stat.isFile() || stat.isSymbolicLink() || stat.size > 1024 * 1024)
        throw new Error('The Qoder Workflow output is not a regular file within the size limit.')
      evidence.outputFile = outputFile
      await testInfo.attach(`qoder-workflow-${failed ? 'error' : 'output'}-output`, { path: outputFile, contentType: 'application/json' })
      evidence.output = JSON.parse(readFileSync(outputFile, 'utf8'))
      await testInfo.attach(`qoder-workflow-${failed ? 'error' : 'output'}-native`, { body: qoderWorkflowDiagnosticJson(evidence), contentType: 'application/json' })
      expect(task?.kind).toBe(BackgroundTaskKind.WORKFLOW)
      expect(initial.agentSessionId).toBe(agent.agentSessionId)
      expect(task?.status).toBe(failed ? BackgroundTaskStatus.FAILED : BackgroundTaskStatus.COMPLETED)
      expect(outcome.status).toBe(failed ? 'failed' : 'completed')
      if (failed)
        expect(outcome.summary).toContain(expected)
      else
        expect(outcome.result).toBe(expected)
      const nativeOutput = qoderWorkflowOutput(evidence.output, launch)
      expect(nativeOutput.status).toBe(failed ? 'failed' : 'completed')
      if (failed)
        expect(nativeOutput.error).toContain(expected)
      else
        expect(nativeOutput.result).toBe(expected)
      runs.push({ launch, failed })
    }, async () => {
      const results = await Promise.allSettled([
        modelScript.status(),
        readNativeMessageSnapshot(context, agent.id).then(snapshot => ({ agentId: snapshot.agentId, agentSessionId: snapshot.agentSessionId, messages: snapshot.messages.map(nativeMessageBody) })),
        readNativeSidebarSnapshot(context, agent.id).then(snapshot => snapshot.backgroundTasks),
      ])
      evidence.finalReads = results.map(result => result.status === 'fulfilled' ? result.value : String(result.reason))
      await testInfo.attach(`qoder-workflow-${failed ? 'error' : 'output'}-diagnostics`, { body: qoderWorkflowDiagnosticJson(evidence), contentType: 'application/json' })
      const errors = results.flatMap(result => result.status === 'rejected' ? [result.reason] : [])
      if (errors.length > 0)
        throw new AggregateError(errors, 'The Qoder Workflow diagnostic reads failed.')
    })
  }
  await page.reload()
  const reloaded = await readNativeMessageSnapshot(context, agent.id)
  const tasks = (await readNativeSidebarSnapshot(context, agent.id)).backgroundTasks
  await testInfo.attach('qoder-workflow-reloaded', { body: JSON.stringify({ messages: reloaded.messages.map(nativeMessageBody), tasks }, null, 2), contentType: 'application/json' })
  expect(reloaded.agentSessionId).toBe(agent.agentSessionId)
  await expandBackgroundTasksSection(page)
  for (const run of runs) {
    const launch = reloaded.messages.map(nativeMessageBody).map(value => qoderWorkflowLaunch(value, run.launch.callId)).find(value => value !== undefined)
    expect(launch).toEqual(run.launch)
    expect(tasks.find(task => task.id === `workflow:${run.launch.sessionId}:${run.launch.callId}`)?.status).toBe(run.failed ? BackgroundTaskStatus.FAILED : BackgroundTaskStatus.COMPLETED)
    const taskId = `workflow:${run.launch.sessionId}:${run.launch.callId}`
    const row = page.locator(`[data-testid="bg-task-row"][data-task-id="${taskId}"]:visible[data-kind="workflow"]`).first()
    await expect(row).toBeVisible()
    await expect(row).toHaveAttribute('data-task-id', taskId)
    await expect(row).toHaveAttribute('data-status', run.failed ? 'failed' : 'completed')
  }
})
