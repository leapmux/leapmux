import { expect } from '@playwright/test'
import { BackgroundTaskKind, BackgroundTaskStatus, MessageSource } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { openNativeCatalogTurn } from '../helpers/nativeCodeExecution'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { expandNativeResultView } from '../helpers/nativeResultView'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { runNativeToolTurn, waitForNativeToolSteps } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { zcodeCreateWorkflowToolCall, zcodeGetWorkflowRunToolCall, zcodeWorkflowSkillToolCall } from '../helpers/providerToolCalls'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { uniqueMarker } from '../helpers/shellArguments'
import { expandBackgroundTasksSection } from '../helpers/subagentRegistry'
import { assistantBubbles, sendMessage, toolCallRow, waitForAgentIdle } from '../helpers/ui'
import { ZCODE_AGENT, zcodeTest } from '../zcode-fixtures'
import { zcodeStoredWorkflowCompletion, zcodeWorkflowCompletion, zcodeWorkflowLaunch } from './codeExecution'
import { nativeContext } from './scenarios'

zcodeTest('runs native workflow scripts and retains computed output and errors after reload', async ({ native }, testInfo) => {
  const { page, modelScript } = native
  const agent = await currentNativeAgent(native)
  const marker = uniqueMarker('NATIVEZCODE')
  await modelScript.fallback({ text: 'The native workflow notification arrived.' })
  for (const failed of [false, true]) {
    const label = failed ? 'error' : 'output'
    const name = `native-code-${label}-${marker}`
    const expected = `${marker}${failed ? 77 : 42}`
    const source = failed ? `phase("Fail"); throw new Error(${JSON.stringify(marker)} + (70 + 7));` : `phase("Compute"); return ${JSON.stringify(marker)} + (40 + 2);`
    expect(source).not.toContain(expected)
    const launchCallId = `native-code-${label}`
    const launchAnswer = `The native ${label} launch returned.`
    const start = await modelScript.queue(
      { toolCalls: [zcodeWorkflowSkillToolCall(`workflow-skill-${label}`)] },
      { toolCalls: [zcodeCreateWorkflowToolCall(launchCallId, name, source)] },
      { text: launchAnswer },
    )
    await sendMessage(page, modelScript.prompt(`Run the native ${label} workflow script.`))
    await waitForNativeToolSteps(native, start + 3)
    const launch = zcodeWorkflowLaunch(nativeToolResult(await modelScript.requestAt(start + 2), launchCallId))
    await retryUntilPass(async () => {
      const snapshot = await readNativeSidebarSnapshot(native, agent.id)
      const tasks = snapshot.backgroundTasks.filter(task => task.id === launchCallId && task.kind === BackgroundTaskKind.WORKFLOW)
      expect(tasks.map(task => task.status), 'the Worker holds one task row of the native ZCode workflow, and the row ended')
        .toEqual([failed ? BackgroundTaskStatus.FAILED : BackgroundTaskStatus.COMPLETED])
    })
    await waitForAgentIdle(page)
    const readCallId = `native-workflow-read-${label}`
    const readAnswer = `The native ${label} read returned.`
    const { resultRequest } = await runNativeToolTurn(native, {
      toolCalls: [zcodeGetWorkflowRunToolCall(readCallId, launch.runId)],
      prompt: `Read the exact completed native ${label} workflow once.`,
      answer: readAnswer,
    })
    const text = nativeToolResult(resultRequest, readCallId)
    const completion = zcodeWorkflowCompletion(text, launch.runId)
    expect(completion.status).toBe(failed ? 'failed' : 'completed')
    expect(completion.text).toContain(expected)
    await testInfo.attach(`zcode-${label}-native-read`, { body: text, contentType: 'text/plain' })
    const prove = async () => {
      const snapshot = await readNativeMessageSnapshot(native, agent.id)
      expect(snapshot.agentSessionId).toBe(agent.agentSessionId)
      const frames = snapshot.messages.filter(message => message.source === MessageSource.AGENT
        && message.agentSessionId === agent.agentSessionId && message.spanId === readCallId).map(nativeMessageBody)
      expect(zcodeStoredWorkflowCompletion(frames, agent.agentSessionId, readCallId, launch.runId)).toEqual(completion)
      const sidebar = await readNativeSidebarSnapshot(native, agent.id)
      const tasks = sidebar.backgroundTasks.filter(task => task.id === launchCallId)
      expect(tasks).toHaveLength(1)
      expect(tasks[0]?.kind).toBe(BackgroundTaskKind.WORKFLOW)
      expect(tasks[0]?.status).toBe(failed ? BackgroundTaskStatus.FAILED : BackgroundTaskStatus.COMPLETED)
      expect(tasks.some(task => task.title.includes(name) || task.groupLabel === name)).toBe(true)
      await expandBackgroundTasksSection(page)
      const row = page.locator(`[data-testid="bg-task-row"][data-kind="workflow"][data-task-id="${launchCallId}"]:visible`).first()
      await expect(row).toHaveCount(1)
      await expect(row).toHaveAttribute('data-status', failed ? 'failed' : 'completed')
      const card = toolCallRow(page, readCallId)
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
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  const request = await openNativeCatalogTurn(context, ZCODE_AGENT, { directoryPrefix: 'native-zcode-code-limit-' })
  const body = isObject(request.body) ? request.body : undefined
  const tools = Array.isArray(body?.tools) ? body.tools.filter(isObject) : []
  const definitions = tools.map(tool => isObject(tool.function) ? tool.function : tool)
  const node = definitions.filter(tool => tool.name === 'mcp__node_repl__js')
  expect(node).toHaveLength(1)
  expect(node[0]?.description).toContain('Browser Use and Computer Use only')
  expect(node[0]?.description).toContain('Do not use it as a general-purpose JavaScript runtime')
  expect(definitions.map(tool => tool.name)).not.toContain('codemode')
})
