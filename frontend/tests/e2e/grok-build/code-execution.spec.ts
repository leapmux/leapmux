import { readFileSync } from 'node:fs'
import { expect } from '@playwright/test'
import { NOTIFICATION_THREAD_TYPE, NOTIFICATION_TYPE } from '../../../src/generated/contracts/worker-vocab'
import { BackgroundTaskKind, BackgroundTaskStatus, MessageSource } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { grokTest } from '../grok-fixtures'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent, nativeModelToolNames } from '../helpers/nativeScenario'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { grokWorkflowToolCall } from '../helpers/providerToolCalls'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { uniqueMarker } from '../helpers/shellArguments'
import { backgroundTaskRows, expandBackgroundTasksSection } from '../helpers/subagentRegistry'
import { assistantBubbles, messageContents, openWorkspace, waitForAgentIdle } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { grokWorkflowCompletion, grokWorkflowLaunch, grokWorkflowManifestPath, grokWorkflowName, grokWorkflowReportLabel, readGrokWorkflowManifest } from './codeExecution'
import { GROK_AGENT, nativeContext } from './scenarios'

grokTest('runs native Rhai scripts and retains computed output and errors after reload', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }, testInfo) => {
  const opened = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, GROK_AGENT, { optionValues: { approvalMode: 'always-approve' } })
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  const agent = await currentNativeAgent(context)
  expect(agent.id).toBe(opened.agentId)
  const home = leapmuxServer.agentEnv.GROK_HOME
  if (!home)
    throw new Error('The native Grok script requires its private profile.')
  const marker = uniqueMarker('NATIVEGROK')
  await modelScript.fallback({ text: 'The native workflow completion arrived.' })
  for (const failed of [false, true]) {
    const label = failed ? 'error' : 'output'
    const name = grokWorkflowName(label, marker)
    const expected = `${marker}${failed ? 77 : 42}`
    const source = `let meta = #{ name: ${JSON.stringify(name)}, description: "Compute one native value." };\n${failed ? `throw ${JSON.stringify(marker)} + (70 + 7).to_string();` : `${JSON.stringify(marker)} + (40 + 2).to_string()`}`
    expect(source).not.toContain(expected)
    const callId = `native-code-${label}`
    const answer = `The native ${label} launch returned.`
    const { toolRequest: catalog, resultRequest: result } = await runNativeToolTurn(context, {
      toolCalls: [grokWorkflowToolCall(callId, source)],
      prompt: `Run the native ${label} script.`,
      answer,
    })
    expect(nativeModelToolNames(catalog)).toContain('workflow')
    const launchSnapshot = await readNativeMessageSnapshot(context, agent.id)
    expect(launchSnapshot.agentSessionId).toBe(agent.agentSessionId)
    const launchFrames = launchSnapshot.messages.filter(message => message.source === MessageSource.AGENT
      && message.agentSessionId === agent.agentSessionId && message.spanId === callId).map(nativeMessageBody)
    await testInfo.attach(`grok-${label}-native-launch`, {
      body: JSON.stringify({ agentId: agent.id, sessionId: agent.agentSessionId, callId, result: nativeToolResult(result, callId), frames: launchFrames }),
      contentType: 'application/json',
    })
    const launch = grokWorkflowLaunch(launchFrames, callId)
    expect(launch.name).toBe(name)
    const path = grokWorkflowManifestPath(launch, home, agent.agentSessionId)
    expect(readFileSync(launch.scriptPath, 'utf8')).toBe(source)
    // Grok writes the manifest while the workflow runs, so a read can find the file absent or half written. The read
    // throws then, and the wait retries it.
    const manifest = await retryUntilPass(() => {
      const read = readGrokWorkflowManifest(path)
      expect(grokWorkflowCompletion(read, launch)?.status, 'the Grok workflow manifest stores the final status').toBe(failed ? 'failed' : 'completed')
      return read
    })
    const completion = grokWorkflowCompletion(manifest, launch)
    if (!isObject(manifest) || !isObject(manifest.state) || typeof manifest.state.objective !== 'string')
      throw new Error('The native Grok workflow manifest has no objective.')
    const reportLabel = grokWorkflowReportLabel(launch.name, manifest.state.objective)
    if (!completion)
      throw new Error('The native Grok script did not store its final result.')
    expect(completion.text).toContain(expected)
    await testInfo.attach(`grok-${label}-native-manifest`, { body: readFileSync(path), contentType: 'application/json' })
    const prove = async () => {
      const messages = await readNativeMessageSnapshot(context, agent.id)
      expect(messages.agentSessionId).toBe(agent.agentSessionId)
      const sidebar = await readNativeSidebarSnapshot(context, agent.id)
      const tasks = sidebar.backgroundTasks.filter(task => task.id === `workflow:${launch.runId}`)
      expect(tasks).toHaveLength(1)
      expect(tasks[0]?.kind).toBe(BackgroundTaskKind.WORKFLOW)
      expect(tasks[0]?.status).toBe(failed ? BackgroundTaskStatus.FAILED : BackgroundTaskStatus.SUCCEEDED)
      expect(tasks[0]?.groupKey).toBe(launch.runId)
      expect(tasks[0]?.groupLabel).toBe(name)
      expect(tasks[0]?.title).toBe(reportLabel)
      const reports = messages.messages.filter(message => message.source === MessageSource.LEAPMUX).flatMap((message) => {
        const body = nativeMessageBody(message)
        if (!isObject(body) || body.type !== NOTIFICATION_THREAD_TYPE || !Array.isArray(body.messages))
          return []
        return body.messages.filter(isObject).filter(item => item.type === NOTIFICATION_TYPE.SubagentReport && item.label === reportLabel)
      })
      expect(reports).toHaveLength(1)
      expect(reports[0]?.status).toBe(failed ? 'failed' : 'succeeded')
      expect(reports[0]?.text).toContain(expected)
      await expandBackgroundTasksSection(page)
      const row = backgroundTaskRows(page, { kind: 'workflow', taskId: `workflow:${launch.runId}` }).first()
      await expect(row).toHaveCount(1)
      await expect(row).toHaveAttribute('data-status', failed ? 'failed' : 'succeeded')
      await expect(messageContents(page).filter({ hasText: expected }).first()).toBeVisible()
      await expect(assistantBubbles(page).filter({ hasText: answer }).first()).toBeVisible()
    }
    await waitForAgentIdle(page)
    await prove()
    await page.reload()
    await prove()
  }
})
