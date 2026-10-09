import { expect } from '@playwright/test'
import { BackgroundTaskKind, BackgroundTaskStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { withCleanup } from '../helpers/cleanup'
import { ruleRequest, stepRequest } from '../helpers/mockModelScript'
import { nativeModelContextText, nativeModelToolNames } from '../helpers/nativeScenario'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { piWorkflowToolCall } from '../helpers/providerToolCalls'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { uniqueMarker } from '../helpers/shellArguments'
import { backgroundTaskRows, expandBackgroundTasksSection } from '../helpers/subagentRegistry'
import { messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { workflowGroupHeading } from '../helpers/workflowGrouping'
import { piTest } from '../pi-fixtures'
import { piWorkflowNoticeRule } from './childNoticeRule'

piTest('runs a native two-stage workflow without workflow grouping or stage rows', async ({ native }) => {
  const { page, modelScript } = native
  const suffix = uniqueMarker()
  const gate = `pi-wf-first-${suffix}`
  const firstAnswer = `ACTUAL_FIRST_WORKFLOW_ANSWER_${suffix}`
  const secondAnswer = `ACTUAL_SECOND_WORKFLOW_ANSWER_${suffix}`
  const firstPrompt = modelScript.prompt(`NATIVE_WF_FIRST_${suffix}: reply once.`)
  const secondPrompt = modelScript.prompt(`NATIVE_WF_SECOND_${suffix}: reply once.`)
  const script = `export const meta = { name: 'Native two-stage workflow', description: 'Run two actual native child stages.', phases: [{ title: 'First' }, { title: 'Second' }] };\nphase('First');\nconst first = await agent(${JSON.stringify(firstPrompt)}, { label: 'first' });\nphase('Second');\nconst second = await agent(${JSON.stringify(secondPrompt)}, { label: 'second' });\nreturn { first, second };`
  const callId = 'native-pi-workflow'
  await withCleanup(async () => {
    await modelScript.rule(
      { name: 'the first actual workflow child replies', when: { user: `^NATIVE_WF_FIRST_${suffix}` }, respond: { text: firstAnswer, gate }, once: true },
      { name: 'the second actual workflow child replies', when: { user: `^NATIVE_WF_SECOND_${suffix}` }, respond: { text: secondAnswer }, once: true },
    )
    const start = await modelScript.queue({ toolCalls: [piWorkflowToolCall(callId, script)] }, { text: 'The native workflow started.' })
    await sendMessage(page, modelScript.prompt('Run the supplied native workflow and report its completed result.'))
    await modelScript.waitForGate(gate)
    const launch = await modelScript.waitForSteps(start + 2)
    expect(nativeModelToolNames(stepRequest(launch, start))).toContain('SubagentWorkflow')
    const acknowledgement = nativeToolResult(stepRequest(launch, start + 1), callId)
    const taskId = /^Task ID: (wf_[^\r\n]+)$/m.exec(acknowledgement)?.[1]
    expect(taskId).toBeTruthy()
    if (!taskId)
      throw new Error('The actual Pi workflow receipt contains no task ID.')
    const scriptPath = /^Script: ([^\r\n]+)$/m.exec(acknowledgement)?.[1]
    await modelScript.rule(piWorkflowNoticeRule({ name: 'the parent receives the actual workflow completion', taskId, callId, workflowName: 'Native two-stage workflow', ...(scriptPath === undefined ? {} : { scriptPath }), reports: [firstAnswer, secondAnswer], reply: 'The actual native workflow result arrived.' }))
    const running = (await readNativeSidebarSnapshot(native)).backgroundTasks.find(task => task.id === taskId)
    expect(running?.status).toBe(BackgroundTaskStatus.RUNNING)
    expect(running?.kind).toBe(BackgroundTaskKind.SUBAGENT)
    expect(running?.groupKey).toBe('')
    expect(running?.childAgentId).toBe('')
    await expandBackgroundTasksSection(page)
    const row = backgroundTaskRows(page).first()
    await expect(row).toHaveAttribute('data-status', 'running')
    expect(await workflowGroupHeading(row)).toBe('')
    await modelScript.releaseGate(gate)
    await retryUntilPass(async () => {
      expect((await readNativeSidebarSnapshot(native)).backgroundTasks.find(task => task.id === taskId)?.status, 'the Worker completes the workflow task')
        .toBe(BackgroundTaskStatus.SUCCEEDED)
    })
    await expect.poll(async () => (await modelScript.status()).ruleMatches['the parent receives the actual workflow completion'] ?? 0).toBeGreaterThan(0)
    const completed = await modelScript.status()
    expect(completed.ruleMatches['the first actual workflow child replies']).toBe(1)
    expect(completed.ruleMatches['the second actual workflow child replies']).toBe(1)
    const nativeContext = nativeModelContextText(ruleRequest(completed, 'the parent receives the actual workflow completion'))
    expect(nativeContext).toContain(firstAnswer)
    expect(nativeContext).toContain(secondAnswer)
    await waitForAgentIdle(page)
    await expect(messageContents(page).filter({ hasText: firstAnswer }).first()).toBeVisible()
    const inspect = async () => {
      await expandBackgroundTasksSection(page)
      const snapshot = await readNativeSidebarSnapshot(native)
      expect(snapshot.backgroundTasks).toHaveLength(1)
      const task = snapshot.backgroundTasks[0]
      expect(task?.id).toBe(taskId)
      expect(task?.status).toBe(BackgroundTaskStatus.SUCCEEDED)
      expect(task?.kind).toBe(BackgroundTaskKind.SUBAGENT)
      expect(task?.groupKey).toBe('')
      expect(task?.groupLabel).toBe('')
      expect(task?.childAgentId).toBe('')
      await expect(row).toHaveAttribute('data-status', 'succeeded')
      expect(await workflowGroupHeading(row)).toBe('')
    }
    await inspect()
    await page.reload()
    await inspect()
  }, async () => {
    await modelScript.releaseGateIfHeld(gate)
  })
})

piTest('rejects an invalid native workflow script without a phantom workflow row', async ({ native }) => {
  const { page, modelScript } = native
  const callId = 'invalid-native-pi-workflow'
  const start = await modelScript.queue({ toolCalls: [piWorkflowToolCall(callId, 'return 0;')] }, { text: 'The native workflow script was refused.' })
  await sendMessage(page, modelScript.prompt('Try the supplied invalid native workflow script once.'))
  const status = await modelScript.waitForSteps(start + 2)
  expect(nativeToolResult(stepRequest(status, start + 1), callId)).toContain('A workflow script must begin with')
  await waitForAgentIdle(page)
  expect((await readNativeSidebarSnapshot(native)).backgroundTasks).toEqual([])
  await expect(messageContents(page).filter({ hasText: 'A workflow script must begin with' }).first()).toBeVisible()
})
