import { randomUUID } from 'node:crypto'
import { expect } from '@playwright/test'
import { AgentProvider, BackgroundTaskKind, BackgroundTaskStatus, MessageSource } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { waitForNativeToolSteps } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { zcodeCreateWorkflowToolCall, zcodeGetWorkflowRunToolCall, zcodeWorkflowSkillToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { expandBackgroundTasksSection } from '../helpers/subagentRegistry'
import { assistantBubbles, openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { zcodeTest } from '../zcode-fixtures'
import { zcodeStoredWorkflowCompletion, zcodeWorkflowCompletion, zcodeWorkflowLaunch } from './codeExecution'

zcodeTest('runs native workflow scripts and retains computed output and errors after reload', async ({ authenticatedZCodeWorkspace, leapmuxServer, page, modelScript }, testInfo) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedZCodeWorkspace.workspaceId, provider: AgentProvider.ZCODE }
  const agent = await currentNativeAgent(context)
  const marker = `NATIVEZCODE${randomUUID().replaceAll('-', '')}`
  await modelScript.fallback({ text: 'The native workflow notification arrived.' })
  for (const failed of [false, true]) {
    const label = failed ? 'error' : 'output'
    const name = `native-code-${label}-${marker}`
    const expected = `${marker}${failed ? 77 : 42}`
    const source = failed ? `phase("Fail"); throw new Error(${JSON.stringify(marker)} + (70 + 7));` : `phase("Compute"); return ${JSON.stringify(marker)} + (40 + 2);`
    expect(source).not.toContain(expected)
    const start = (await modelScript.status()).stepCount
    const launchCallId = `native-code-${label}`
    const launchAnswer = `The native ${label} launch returned.`
    await modelScript.queue(
      { toolCalls: [zcodeWorkflowSkillToolCall(`workflow-skill-${label}`)] },
      { toolCalls: [zcodeCreateWorkflowToolCall(launchCallId, name, source)] },
      { text: launchAnswer },
    )
    await sendMessage(page, modelScript.prompt(`Run the native ${label} workflow script.`))
    await waitForNativeToolSteps(context, start + 3)
    const launchRequest = (await modelScript.waitForSteps(start + 3)).requests.find(item => item.stepIndex === start + 2)
    if (!launchRequest)
      throw new Error('The native ZCode workflow has no exact launch result.')
    const launch = zcodeWorkflowLaunch(nativeToolResult(launchRequest, launchCallId))
    await expect.poll(async () => {
      const snapshot = await readNativeSidebarSnapshot(context, agent.id)
      const tasks = snapshot.backgroundTasks.filter(task => task.id === launchCallId && task.kind === BackgroundTaskKind.WORKFLOW)
      if (tasks.length > 1)
        throw new Error('The native ZCode workflow has repeated task rows.')
      return tasks[0]?.status
    }).toBe(failed ? BackgroundTaskStatus.FAILED : BackgroundTaskStatus.COMPLETED)
    await waitForAgentIdle(page)
    const readStart = (await modelScript.status()).stepCount
    const readCallId = `native-workflow-read-${label}`
    const readAnswer = `The native ${label} read returned.`
    await modelScript.queue({ toolCalls: [zcodeGetWorkflowRunToolCall(readCallId, launch.runId)] }, { text: readAnswer })
    await sendMessage(page, modelScript.prompt(`Read the exact completed native ${label} workflow once.`))
    await waitForNativeToolSteps(context, readStart + 2)
    const readRequest = (await modelScript.waitForSteps(readStart + 2)).requests.find(item => item.stepIndex === readStart + 1)
    if (!readRequest)
      throw new Error('The native ZCode workflow has no exact final read result.')
    const text = nativeToolResult(readRequest, readCallId)
    const completion = zcodeWorkflowCompletion(text, launch.runId)
    expect(completion.status).toBe(failed ? 'failed' : 'completed')
    expect(completion.text).toContain(expected)
    await testInfo.attach(`zcode-${label}-native-read`, { body: text, contentType: 'text/plain' })
    const prove = async () => {
      const snapshot = await readNativeMessageSnapshot(context, agent.id)
      expect(snapshot.agentSessionId).toBe(agent.agentSessionId)
      const frames = snapshot.messages.filter(message => message.source === MessageSource.AGENT
        && message.agentSessionId === agent.agentSessionId && message.spanId === readCallId).map(nativeMessageBody)
      expect(zcodeStoredWorkflowCompletion(frames, agent.agentSessionId, readCallId, launch.runId)).toEqual(completion)
      const sidebar = await readNativeSidebarSnapshot(context, agent.id)
      const tasks = sidebar.backgroundTasks.filter(task => task.id === launchCallId)
      expect(tasks).toHaveLength(1)
      expect(tasks[0]?.kind).toBe(BackgroundTaskKind.WORKFLOW)
      expect(tasks[0]?.status).toBe(failed ? BackgroundTaskStatus.FAILED : BackgroundTaskStatus.COMPLETED)
      expect(tasks.some(task => task.title.includes(name) || task.groupLabel === name)).toBe(true)
      await expandBackgroundTasksSection(page)
      const row = page.locator(`[data-testid="bg-task-row"][data-kind="workflow"][data-task-id="${launchCallId}"]:visible`).first()
      await expect(row).toHaveCount(1)
      await expect(row).toHaveAttribute('data-status', failed ? 'failed' : 'completed')
      const card = page.locator(`[data-testid="message-bubble"][data-tool-call-id="${readCallId}"][data-tool-row-role="result"]:visible`)
      await expect(card).toHaveCount(1)
      await expandNativeResultView(card)
      await expect(card).toContainText(expected)
      await expect(assistantBubbles(page).filter({ hasText: readAnswer }).first()).toBeVisible()
    }
    await prove()
    await page.reload()
    await prove()
  }
})

zcodeTest('restricts the native Node executor to browser and computer use', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.ZCODE }
  await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, context.workspaceId, createTestDirectory('native-zcode-code-limit-'), { agentProvider: context.provider, ...agentOpenOptions(agentSettings(context.provider)) })
  await openWorkspace(page, context.workspaceId)
  const request = await sendNativeAnswer(context, 'Reply once while the restricted native executor catalog remains available.', 'The native restricted executor catalog turn completed.')
  const body = isObject(request.body) ? request.body : undefined
  const tools = Array.isArray(body?.tools) ? body.tools.filter(isObject) : []
  const definitions = tools.map(tool => isObject(tool.function) ? tool.function : tool)
  const node = definitions.filter(tool => tool.name === 'mcp__node_repl__js')
  expect(node).toHaveLength(1)
  expect(node[0]?.description).toContain('Browser Use and Computer Use only')
  expect(node[0]?.description).toContain('Do not use it as a general-purpose JavaScript runtime')
  expect(definitions.map(tool => tool.name)).not.toContain('codemode')
})
