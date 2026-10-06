import { expect } from '@playwright/test'
import { AgentProvider, BackgroundTaskKind, BackgroundTaskStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { ohMyPiYieldToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { expandBackgroundTasksSection } from '../helpers/subagentRegistry'
import { applyPermissionPreset, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { workflowGroupHeading } from '../helpers/workflowGrouping'
import { ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest('keeps two actual native task children as separate ungrouped rows', async ({ page, modelScript, leapmuxServer, authenticatedOhMyPiWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedOhMyPiWorkspace.workspaceId, provider: AgentProvider.OH_MY_PI }
  await applyPermissionPreset(page, 'bypass')
  for (const marker of ['FIRST', 'SECOND']) {
    await modelScript.rule({ name: `omp ungrouped ${marker}`, when: { user: `OMPGROUP${marker}`, body: '"name":"yield"' }, respond: { toolCalls: [ohMyPiYieldToolCall(`omp-yield-${marker}`, `OMP_NATIVE_GROUP_REPORT_${marker}`)] }, once: true })
  }
  await modelScript.queue(
    { toolCalls: ['FIRST', 'SECOND'].map(marker => spawnSubagentToolCall(AgentProvider.OH_MY_PI, `omp-task-${marker}`, { description: `Run the ${marker.toLowerCase()} group task`, prompt: modelScript.prompt(`OMPGROUP${marker} Report the requested result.`) })) },
    { text: 'The parent consumed both actual native task reports.' },
  )
  await sendMessage(page, modelScript.prompt('Run both native task children and collect both reports.'))
  const status = await modelScript.waitForSteps(2)
  await waitForAgentIdle(page)
  const request = status.requests.find(value => value.stepIndex === 1)
  for (const marker of ['FIRST', 'SECOND']) {
    expect(status.ruleMatches[`omp ungrouped ${marker}`]).toBe(1)
    expect(nativeToolResult(request, `omp-task-${marker}`)).toContain(`OMP_NATIVE_GROUP_REPORT_${marker}`)
  }
  for (const reload of [false, true]) {
    if (reload)
      await page.reload()
    const snapshot = await readNativeSidebarSnapshot(context)
    const tasks = snapshot.backgroundTasks.filter(task => task.kind === BackgroundTaskKind.SUBAGENT)
    expect(tasks).toHaveLength(2)
    for (const task of tasks) {
      expect(task.status).toBe(BackgroundTaskStatus.COMPLETED)
      expect(task.childAgentId).not.toBe('')
      expect(task.groupKey).toBe('')
      expect(task.groupLabel).toBe('')
    }
    expect(new Set(tasks.map(task => task.childAgentId)).size).toBe(2)
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
