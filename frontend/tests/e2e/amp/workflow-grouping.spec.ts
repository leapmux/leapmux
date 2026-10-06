import { expect } from '@playwright/test'
import { AgentProvider, BackgroundTaskKind, BackgroundTaskStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { ampTest } from '../amp-fixtures'
import { ampToolResultReader } from '../helpers/ampToolResult'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { expandBackgroundTasksSection } from '../helpers/subagentRegistry'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { workflowGroupHeading } from '../helpers/workflowGrouping'

ampTest('keeps two actual remote Tasks as separate rows without a workflow group', async ({ page, modelScript, leapmuxServer, authenticatedAmpWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedAmpWorkspace.workspaceId, provider: AgentProvider.AMP }
  for (const marker of ['FIRST', 'SECOND']) {
    await modelScript.rule({ name: `amp ungrouped ${marker}`, when: { user: `AMPWORKFLOW${marker}` }, respond: { text: `AMP_NATIVE_GROUP_REPORT_${marker}` }, once: true })
  }
  await modelScript.queue(
    { toolCalls: ['FIRST', 'SECOND'].map(marker => spawnSubagentToolCall(AgentProvider.AMP, `amp-workflow-${marker}`, { description: `Run the ${marker.toLowerCase()} group task`, prompt: modelScript.prompt(`AMPWORKFLOW${marker} Report the requested result.`) })) },
    { text: 'The parent consumed both actual remote task reports.' },
  )
  await sendMessage(page, modelScript.prompt('Run both remote Tasks and collect both reports.'))
  const status = await modelScript.waitForSteps(2)
  await waitForAgentIdle(page)
  const request = status.requests.find(value => value.stepIndex === 1)
  if (!request)
    throw new Error('The actual remote Tasks produced no native parent result request.')
  for (const marker of ['FIRST', 'SECOND']) {
    expect(status.ruleMatches[`amp ungrouped ${marker}`]).toBe(1)
    expect((await ampToolResultReader(context)(request, `amp-workflow-${marker}`)).text).toContain(`AMP_NATIVE_GROUP_REPORT_${marker}`)
  }
  for (const reload of [false, true]) {
    if (reload)
      await page.reload()
    const snapshot = await readNativeSidebarSnapshot(context)
    const tasks = snapshot.backgroundTasks.filter(task => task.kind === BackgroundTaskKind.SUBAGENT)
    expect(tasks).toHaveLength(2)
    for (const task of tasks) {
      expect(task.status).toBe(BackgroundTaskStatus.COMPLETED)
      expect(task.childAgentId).toBe('')
      expect(task.groupKey).toBe('')
      expect(task.groupLabel).toBe('')
    }
    expect(snapshot.backgroundTasks.some(task => task.kind === BackgroundTaskKind.WORKFLOW)).toBe(false)
    await expandBackgroundTasksSection(page)
    const rows = page.locator('[data-testid="bg-task-row"]:visible[data-kind="subagent"]')
    await expect(rows).toHaveCount(2)
    await expect(rows.nth(0)).toHaveAttribute('data-status', 'completed')
    await expect(rows.nth(1)).toHaveAttribute('data-status', 'completed')
    expect(await workflowGroupHeading(rows.nth(0))).toBe('')
    expect(await workflowGroupHeading(rows.nth(1))).toBe('')
  }
})
