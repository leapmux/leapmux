import type { ClaudeWorkflowLaunch } from './codeExecution'
import { existsSync, lstatSync, readdirSync, readFileSync } from 'node:fs'
import { basename, join } from 'node:path'
import { BackgroundTaskKind, BackgroundTaskStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CLAUDE_AGENT, claudeTest, expect } from '../claude-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { openNativeCatalogTurn } from '../helpers/nativeCodeExecution'
import { assertPrivateNativePath } from '../helpers/nativeCredentialIsolation'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent, nativeModelToolNames } from '../helpers/nativeScenario'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { waitForNativeToolSteps } from '../helpers/nativeToolExecution'
import { claudeWorkflowToolCall } from '../helpers/providerToolCalls'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { getGlobalState } from '../helpers/server'
import { uniqueMarker } from '../helpers/shellArguments'
import { expandBackgroundTasksSection } from '../helpers/subagentRegistry'
import { sendMessage } from '../helpers/ui'
import { claudeWorkflowLaunch, claudeWorkflowModelOutcome, claudeWorkflowOutput, claudeWorkflowOutputFile, claudeWorkflowSnapshot } from './codeExecution'
import { nativeContext } from './scenarios'

claudeTest('executes native Workflow code and preserves the computed result and script error', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  const catalogRequest = await openNativeCatalogTurn(context, CLAUDE_AGENT, { directoryPrefix: 'native-workflow-code-' })
  expect(nativeModelToolNames(catalogRequest)).toContain('Workflow')
  await testInfo.attach('claude-workflow-catalog', { body: JSON.stringify(catalogRequest, null, 2), contentType: 'application/json' })
  const agent = await currentNativeAgent(context)
  const marker = uniqueMarker('NATIVEWORKFLOW')
  // Workflow completion can start another parent turn after the launch turn ends.
  await modelScript.fallback({ text: 'The native Workflow notification arrived.' })
  const runs: { launch: ClaudeWorkflowLaunch, failed: boolean }[] = []
  for (const failed of [false, true]) {
    const callId = `workflow-code-${failed ? 'error' : 'output'}`
    const name = `native-code-${failed ? 'error' : 'output'}-${marker}`
    const source = [
      `export const meta = { name: ${JSON.stringify(name)}, description: 'Compute one local value.' };`,
      failed ? `throw new Error(${JSON.stringify(marker)} + (70 + 7));` : `return ${JSON.stringify(marker)} + (40 + 2);`,
    ].join('\n')
    const expected = `${marker}${failed ? 77 : 42}`
    expect(source).not.toContain(expected)
    const start = await modelScript.queue({ toolCalls: [claudeWorkflowToolCall(callId, source)] }, { text: 'The native Workflow launch turn ended.' })
    const evidence: Record<string, unknown> = { callId, source, agentId: agent.id, agentSessionId: agent.agentSessionId }
    await withCleanup(async () => {
      await sendMessage(page, modelScript.prompt(`Run the native Workflow ${failed ? 'error' : 'output'} script.`))
      await waitForNativeToolSteps(context, start + 2)
      const initial = await readNativeMessageSnapshot(context, agent.id)
      evidence.messages = initial.messages.map(nativeMessageBody)
      evidence.tasks = (await readNativeSidebarSnapshot(context, agent.id)).backgroundTasks
      const launches = initial.messages.map(nativeMessageBody).flatMap((value) => {
        const launch = claudeWorkflowLaunch(value, callId)
        return launch ? [launch] : []
      })
      if (launches.length !== 1)
        throw new Error(`The Claude Workflow has ${launches.length} exact native launch receipts.`)
      const launch = launches[0]!
      evidence.launch = launch
      await retryUntilPass(async () => {
        const tasks = (await readNativeSidebarSnapshot(context, agent.id)).backgroundTasks
        evidence.tasks = tasks
        expect(Boolean(tasks.find(task => task.id === launch.taskId)?.endedAt), 'the Worker ends the task row of the Claude Workflow').toBe(true)
      })
      await expect.poll(async () => {
        const status = await modelScript.status()
        evidence.modelRequests = status.requests
        return claudeWorkflowModelOutcome(status.requests.map(request => request.body), launch) !== undefined
      }).toBe(true)
      const modelStatus = await modelScript.status()
      evidence.modelRequests = modelStatus.requests
      const outcome = claudeWorkflowModelOutcome(modelStatus.requests.map(request => request.body), launch)
      if (!outcome)
        throw new Error('The Claude Workflow lost its exact final native notification after the wait.')
      evidence.outcome = outcome
      const tasks = (await readNativeSidebarSnapshot(context, agent.id)).backgroundTasks
      const task = tasks.find(value => value.id === launch.taskId)
      evidence.task = task
      await testInfo.attach(`claude-workflow-${failed ? 'error' : 'output'}-native-pre-full-output`, { body: JSON.stringify(evidence, null, 2), contentType: 'application/json' })
      const config = leapmuxServer.agentEnv.CLAUDE_CONFIG_DIR
      if (!config)
        throw new Error('The private Claude configuration directory is absent.')
      const projects = join(config, 'projects')
      assertPrivateNativePath(projects, getGlobalState().tmpDir)
      const snapshots = readdirSync(projects, { withFileTypes: true }).filter(entry => entry.isDirectory()).map(entry => join(projects, entry.name, agent.agentSessionId, 'workflows', `${launch.runId}.json`)).filter(existsSync)
      evidence.snapshotPaths = snapshots
      const snapshotValues = snapshots.map((path) => {
        assertPrivateNativePath(path, getGlobalState().tmpDir)
        const stat = lstatSync(path)
        if (!stat.isFile() || stat.isSymbolicLink() || stat.size > 1024 * 1024)
          throw new Error('The Claude Workflow snapshot is not a regular file within the size limit.')
        const value: unknown = JSON.parse(readFileSync(path, 'utf8'))
        return { path, value }
      })
      for (const snapshot of snapshotValues)
        await testInfo.attach(`claude-workflow-${failed ? 'error' : 'output'}-snapshot`, { path: snapshot.path, contentType: 'application/json' })
      evidence.snapshots = snapshotValues
      if (existsSync(outcome.outputFile)) {
        if (basename(outcome.outputFile) !== `${launch.taskId}.output`)
          throw new Error('The Claude Workflow output path does not match its task.')
        if (task?.description !== outcome.outputFile)
          throw new Error('The Claude Workflow registry does not bind the advertised output file.')
        const stat = lstatSync(outcome.outputFile)
        if (!stat.isFile() || stat.isSymbolicLink() || stat.size > 1024 * 1024)
          throw new Error('The Claude Workflow output is not a regular file within the size limit.')
        evidence.outputFile = outcome.outputFile
        await testInfo.attach(`claude-workflow-${failed ? 'error' : 'output'}-output`, { path: outcome.outputFile, contentType: 'application/json' })
        const outputText = readFileSync(outcome.outputFile, 'utf8')
        evidence.outputText = outputText
        evidence.output = claudeWorkflowOutputFile(outputText, outcome.status)
      }
      await testInfo.attach(`claude-workflow-${failed ? 'error' : 'output'}-native`, { body: JSON.stringify(evidence, null, 2), contentType: 'application/json' })
      expect(task?.kind).toBe(BackgroundTaskKind.WORKFLOW)
      expect(initial.agentSessionId).toBe(agent.agentSessionId)
      expect(task?.status).toBe(failed ? BackgroundTaskStatus.FAILED : BackgroundTaskStatus.COMPLETED)
      expect(outcome.status).toBe(failed ? 'failed' : 'completed')
      if (failed)
        expect(outcome.summary).toContain(expected)
      else
        expect(outcome.result).toBe(expected)
      for (const snapshot of snapshotValues) {
        const finalSnapshot = claudeWorkflowSnapshot(snapshot.value, launch)
        expect(finalSnapshot.status).toBe(failed ? 'failed' : 'completed')
        if (failed)
          expect(finalSnapshot.error).toContain(expected)
        else
          expect(finalSnapshot.result).toBe(expected)
      }
      if (!failed && evidence.output !== undefined)
        expect(claudeWorkflowOutput(evidence.output).result).toBe(expected)
      runs.push({ launch, failed })
    }, async () => {
      const results = await Promise.allSettled([
        modelScript.status(),
        readNativeMessageSnapshot(context, agent.id).then(snapshot => ({ agentId: snapshot.agentId, agentSessionId: snapshot.agentSessionId, messages: snapshot.messages.map(nativeMessageBody) })),
        readNativeSidebarSnapshot(context, agent.id).then(snapshot => snapshot.backgroundTasks),
      ])
      evidence.finalReads = results.map(result => result.status === 'fulfilled' ? result.value : String(result.reason))
      await testInfo.attach(`claude-workflow-${failed ? 'error' : 'output'}-diagnostics`, { body: JSON.stringify(evidence, null, 2), contentType: 'application/json' })
      const errors = results.flatMap(result => result.status === 'rejected' ? [result.reason] : [])
      if (errors.length > 0)
        throw new AggregateError(errors, 'The Claude Workflow diagnostic reads failed.')
    })
  }
  await page.reload()
  const reloaded = await readNativeMessageSnapshot(context, agent.id)
  const tasks = (await readNativeSidebarSnapshot(context, agent.id)).backgroundTasks
  await testInfo.attach('claude-workflow-reloaded', { body: JSON.stringify({ messages: reloaded.messages.map(nativeMessageBody), tasks }, null, 2), contentType: 'application/json' })
  expect(reloaded.agentSessionId).toBe(agent.agentSessionId)
  await expandBackgroundTasksSection(page)
  for (const run of runs) {
    const launch = reloaded.messages.map(nativeMessageBody).map(value => claudeWorkflowLaunch(value, run.launch.callId)).find(value => value !== undefined)
    expect(launch).toEqual(run.launch)
    expect(tasks.find(task => task.id === run.launch.taskId)?.status).toBe(run.failed ? BackgroundTaskStatus.FAILED : BackgroundTaskStatus.COMPLETED)
    const row = page.locator(`[data-testid="bg-task-row"][data-task-id="${run.launch.taskId}"]:visible[data-kind="workflow"]`).first()
    await expect(row).toBeVisible()
    await expect(row).toHaveAttribute('data-task-id', run.launch.taskId)
    await expect(row).toHaveAttribute('data-status', run.failed ? 'failed' : 'completed')
  }
})
