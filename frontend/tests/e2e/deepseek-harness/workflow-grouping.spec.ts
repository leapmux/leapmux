import { expect } from '@playwright/test'
import { BackgroundTaskKind } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { finishCleanup, withCleanup } from '../helpers/cleanup'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { codeExecutionToolCall } from '../helpers/providerToolCalls'
import { expandBackgroundTasksSection } from '../helpers/subagentRegistry'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expectRowsInWorkflowGroup } from '../helpers/workflowGrouping'

deepseekHarnessTest('groups two actual one-shot children under their native workflow run and keeps that group after reload', async ({ native }) => {
  const { page, modelScript } = native
  const parent = await currentNativeAgent(native)
  const firstGate = 'native-workflow-first'
  const secondGate = 'native-workflow-second'
  await withCleanup(async () => {
    await modelScript.rule(
      { name: 'the first native workflow child', when: { lastMessage: { role: 'user', text: 'DEEPSEEKWORKFLOWFIRST' } }, once: true, respond: { text: 'The first native child completed.', gate: firstGate } },
      { name: 'the second native workflow child', when: { lastMessage: { role: 'user', text: 'DEEPSEEKWORKFLOWSECOND' } }, once: true, respond: { text: 'The second native child completed.', gate: secondGate } },
    )
    const first = modelScript.prompt('DEEPSEEKWORKFLOWFIRST complete the first actual assignment.')
    const second = modelScript.prompt('DEEPSEEKWORKFLOWSECOND complete the second actual assignment.')
    const source = `return await parallel([() => agent(${JSON.stringify(first)}, {label:"First native work"}), () => agent(${JSON.stringify(second)}, {label:"Second native work"})]);`
    const start = await modelScript.queue(
      { toolCalls: [codeExecutionToolCall(native.provider, 'native-workflow-run', source)] },
      { text: 'The actual native workflow completed.' },
    )
    await sendMessage(page, modelScript.prompt('Execute the actual native workflow with its two child assignments.'))
    await modelScript.waitForGate(firstGate)
    await modelScript.waitForGate(secondGate)
    await expandBackgroundTasksSection(page)
    const children = page.locator('[data-testid="bg-task-row"]:visible[data-kind="subagent"]')
    await expect(children).toHaveCount(2)
    // No spec states the full heading text, so the pattern requires only `native-code` inside it.
    await expectRowsInWorkflowGroup([children.nth(0), children.nth(1)], /native-code/)
    const running = await readNativeSidebarSnapshot(native, parent.id)
    const run = running.backgroundTasks.find(task => task.kind === BackgroundTaskKind.WORKFLOW)
    expect(run).toBeDefined()
    if (!run?.groupKey)
      throw new Error('The native workflow has no exact stored group identity.')
    const owned = running.backgroundTasks.filter(task => task.kind === BackgroundTaskKind.SUBAGENT)
    expect(owned).toHaveLength(2)
    expect(new Set(owned.map(task => task.childAgentId)).size).toBe(2)
    expect(owned.every(task => task.groupKey === run.groupKey && task.groupLabel === run.groupLabel)).toBe(true)
    await Promise.all([modelScript.releaseGate(firstGate), modelScript.releaseGate(secondGate)])
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    const status = await modelScript.status()
    expect(status.ruleMatches['the first native workflow child']).toBe(1)
    expect(status.ruleMatches['the second native workflow child']).toBe(1)
    await expect(assistantBubbles(page).filter({ hasText: 'The actual native workflow completed.' }).first()).toBeVisible()
    for (const row of await children.all())
      await expect(row).toHaveAttribute('data-status', 'completed')
    await page.reload()
    await expandBackgroundTasksSection(page)
    await expect(children).toHaveCount(2)
    await expectRowsInWorkflowGroup([children.nth(0), children.nth(1)], /native-code/)
    const restored = await readNativeSidebarSnapshot(native, parent.id)
    expect(restored.backgroundTasks.map(task => ({ id: task.id, groupKey: task.groupKey, groupLabel: task.groupLabel }))).toEqual(running.backgroundTasks.map(task => ({ id: task.id, groupKey: task.groupKey, groupLabel: task.groupLabel })))
  }, () => finishCleanup([modelScript.releaseGateIfHeld(firstGate), modelScript.releaseGateIfHeld(secondGate)]))
})
