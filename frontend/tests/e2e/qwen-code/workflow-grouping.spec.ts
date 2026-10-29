import type { NativeAgentOpenOptions } from '../helpers/nativeAgentOpen'
import { expect } from '@playwright/test'
import { qwenRegistryRowKey } from '../../../src/components/chat/providers/qwen/registryKey'
import { OPTION_ID_PERMISSION_MODE } from '../../../src/components/chat/settingsGroups'
import { QWEN_TOOL } from '../../../src/generated/contracts/qwen-protocol'
import { BackgroundTaskKind, BackgroundTaskStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { openNativeAgent } from '../helpers/nativeAgentOpen'
import { waitForNativeInputQueueIdle } from '../helpers/nativeInputQueueIdle'
import { currentNativeAgent, nativeAgentById } from '../helpers/nativeScenario'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { qwenWorkflowToolCall } from '../helpers/providerToolCalls'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { backgroundTaskRows, expandBackgroundTasksSection, expectRowBecomesFinal } from '../helpers/subagentRegistry'
import { assistantBubbles, inputQueue, interruptButton, resumePausedQueue, sendMessage, visibleOnly, waitForAgentIdle } from '../helpers/ui'
import { expectRowsInWorkflowGroup } from '../helpers/workflowGrouping'
import { qwenTest } from '../qwen-fixtures'
import { qwenChildTurn } from './childScenario'
import { nativeContext } from './scenarios'

/** How the agent of each test opens: in YOLO mode, so its workflow children run with no permission request. */
const YOLO_OPEN: NativeAgentOpenOptions = {
  overrides: {
    optionValues: { [OPTION_ID_PERMISSION_MODE]: 'yolo' },
  },
}

qwenTest('shows each agent of a native workflow run under the run group', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
  const context = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  })
  await openNativeAgent(context, YOLO_OPEN)
  const workUnit = (label: string, task: string, answer: string) => ({
    label,
    task,
    answer,
    prompt: modelScript.prompt(task),
  })
  const children = [
    workUnit('First native unit', 'Reply with FIRSTNATIVEWORKUNIT.', 'FIRSTNATIVEWORKUNIT'),
    workUnit('Second native unit', 'Reply with SECONDNATIVEWORKUNIT.', 'SECONDNATIVEWORKUNIT'),
  ] as const
  for (const [index, child] of children.entries()) {
    await modelScript.rule({
      name: `native-work-unit-${index}`,
      when: qwenChildTurn(child.task),
      respond: { text: child.answer },
      once: true,
    })
  }
  const [first, second] = children
  const start = await modelScript.queue({
    toolCalls: [
      qwenWorkflowToolCall('two-workflow-units', [
        'export const meta = { name: "qwen-two-work-units", description: "Run two independent assignments." }',
        'phase("Two native assignments")',
        `const first = await agent(${JSON.stringify(first.prompt)}, { label: ${JSON.stringify(first.label)} })`,
        `const second = await agent(${JSON.stringify(second.prompt)}, { label: ${JSON.stringify(second.label)} })`,
        'return { first, second }',
      ].join('\n')),
    ],
  })
  await modelScript.fallback({ text: 'Both native workflow units completed.' })
  await sendMessage(page, modelScript.prompt('Run the two scripted native workflow assignments.'))
  await modelScript.waitForSteps(start + 1)

  await expandBackgroundTasksSection(page)
  const workflowRows = backgroundTaskRows(page, { kind: 'workflow' })
  const runRow = workflowRows.filter({ hasText: 'qwen-two-work-units' }).first()
  await expect(runRow).toBeVisible()
  await expectRowBecomesFinal(page, runRow)
  await expect(runRow).toHaveAttribute('data-status', 'succeeded')
  const agentRows = backgroundTaskRows(page, { kind: 'subagent' })
  await expect(agentRows).toHaveCount(2)
  const firstRow = agentRows.filter({ hasText: 'First native unit' }).first()
  const secondRow = agentRows.filter({ hasText: 'Second native unit' }).first()
  for (const row of [firstRow, secondRow]) {
    await expectRowBecomesFinal(page, row)
    await expect(row).toHaveAttribute('data-status', 'succeeded')
    await expect(row).toHaveAttribute('data-child-agent-id', '')
  }
  // One heading groups the run row and both agent rows.
  await expectRowsInWorkflowGroup([runRow, firstRow, secondRow], 'qwen-two-work-units')
  const status = await modelScript.status()
  expect(status.ruleMatches['native-work-unit-0']).toBe(1)
  expect(status.ruleMatches['native-work-unit-1']).toBe(1)
})

qwenTest('keeps the outgoing workflow stopped when a replacement starts new work', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
  const context = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  })
  const { agentId } = await openNativeAgent(context, YOLO_OPEN)
  const outgoing = {
    name: 'qwen-outgoing-workflow',
    callId: 'outgoing-workflow-before-clear',
    label: 'Outgoing held assignment',
    task: 'Reply with OUTGOINGWORKFLOWCHILD.',
    gate: 'qwen-outgoing-workflow-child',
  }
  const replacement = {
    name: 'qwen-replacement-workflow',
    callId: 'replacement-workflow-after-clear',
    gate: 'qwen-replacement-workflow-child',
    children: [
      { label: 'Replacement first assignment', task: 'Reply with REPLACEMENTFIRSTCHILD.', answer: 'REPLACEMENTFIRSTCHILD' },
      { label: 'Replacement second assignment', task: 'Reply with REPLACEMENTSECONDCHILD.', answer: 'REPLACEMENTSECONDCHILD' },
    ] as const,
  }
  const readGroup = async (runKey: string, labels: string[], status: BackgroundTaskStatus, expectedLabel?: string) => retryUntilPass(async () => {
    const snapshot = await readNativeSidebarSnapshot(context, agentId)
    // Inline workflows expose meta.name only after the script returns.
    // The session-qualified key identifies the group while its child is held.
    const tasks = snapshot.backgroundTasks.filter(task => task.groupKey === runKey)
    expect(tasks).toHaveLength(labels.length + 1)
    const workflows = tasks.filter(task => task.kind === BackgroundTaskKind.WORKFLOW)
    expect(workflows.map(task => task.id)).toEqual([runKey])
    const workflow = workflows[0]
    if (!workflow)
      throw new Error('The Worker workflow group has no run row.')
    expect(workflow.groupLabel.trim()).not.toBe('')
    if (expectedLabel !== undefined)
      expect(workflow.groupLabel).toBe(expectedLabel)
    expect(workflow.title).toBe(workflow.groupLabel)
    expect(tasks.filter(task => task.kind === BackgroundTaskKind.SUBAGENT).map(task => task.title).sort()).toEqual([...labels].sort())
    for (const task of tasks) {
      expect(task.groupKey).toBe(runKey)
      expect(task.groupLabel).toBe(workflow.groupLabel)
      expect(task.status).toBe(status)
    }
    return { tasks, label: workflow.groupLabel }
  })
  await modelScript.rule({
    name: outgoing.gate,
    when: qwenChildTurn(outgoing.task),
    // /clear retires the outgoing native session and workflow while this response is held.
    // No later child turn or parent continuation is admitted for that session.
    respond: { text: 'OUTGOINGWORKFLOWCHILD', gate: outgoing.gate },
    once: true,
  })
  const oldStart = await modelScript.queue({
    toolCalls: [
      qwenWorkflowToolCall(outgoing.callId, [
        `export const meta = { name: ${JSON.stringify(outgoing.name)}, description: "Hold the outgoing assignment." }`,
        'phase("Outgoing assignment")',
        `return await agent(${JSON.stringify(modelScript.prompt(outgoing.task))}, { label: ${JSON.stringify(outgoing.label)} })`,
      ].join('\n')),
    ],
  })
  try {
    await sendMessage(page, modelScript.prompt('Start the outgoing native workflow and hold its child.'))
    await modelScript.waitForSteps(oldStart + 1)
    await modelScript.waitForGate(outgoing.gate)
    const oldRoot = await currentNativeAgent(context)
    expect(oldRoot.id).toBe(agentId)
    expect(oldRoot.agentSessionId).not.toBe('')
    const oldRunKey = qwenRegistryRowKey(QWEN_TOOL.Workflow, oldRoot.agentSessionId, outgoing.callId)
    if (oldRunKey === undefined)
      throw new Error('The outgoing Qwen workflow has no native registry identity.')
    const oldGroup = await readGroup(oldRunKey, [outgoing.label], BackgroundTaskStatus.RUNNING)
    await expandBackgroundTasksSection(page)
    const oldRows = oldGroup.tasks.map(task => backgroundTaskRows(page, { taskId: task.id }).first())
    for (const row of oldRows)
      await expect(row).toHaveAttribute('data-status', 'running')
    await expectRowsInWorkflowGroup(oldRows, oldGroup.label)

    // Clear waits for the current turn, and Interrupt pauses its queue.
    // Await the native stopped rows before resuming clear, so the final task
    // snapshot cannot race session replacement.
    await sendMessage(page, '/clear')
    const queuedClear = inputQueue(page).getByText('/clear', { exact: true })
    await expect(queuedClear).toBeVisible()
    await interruptButton(page).click()
    await waitForNativeInputQueueIdle(leapmuxServer, agentId, modelScript.testDeadline)
    const stoppedGroup = await readGroup(oldRunKey, [outgoing.label], BackgroundTaskStatus.STOPPED)
    for (const task of stoppedGroup.tasks)
      expect(task.endedAt).not.toBe('')
    for (const row of oldRows)
      await expect(row).toHaveAttribute('data-status', 'stopped')
    await expectRowsInWorkflowGroup(oldRows, stoppedGroup.label)
    await resumePausedQueue(page)
    await expect(visibleOnly(page.getByText('Context cleared', { exact: true })).first()).toBeVisible()
    await expect(queuedClear).toHaveCount(0)
    const newRoot = await retryUntilPass(async () => {
      const root = await nativeAgentById(context, agentId)
      expect(root?.agentSessionId).toBeTruthy()
      expect(root?.agentSessionId).not.toBe(oldRoot.agentSessionId)
      if (!root)
        throw new Error('The Worker lost the Qwen root during context clear.')
      return root
    })
    await readGroup(oldRunKey, [outgoing.label], BackgroundTaskStatus.STOPPED, stoppedGroup.label)
    for (const row of oldRows)
      await expect(row).toHaveAttribute('data-status', 'stopped')
    await expectRowsInWorkflowGroup(oldRows, stoppedGroup.label)

    for (const [index, child] of replacement.children.entries()) {
      await modelScript.rule({
        name: `replacement-native-work-unit-${index}`,
        when: qwenChildTurn(child.task),
        respond: {
          text: child.answer,
          ...(index === 0
            ? { gate: replacement.gate }
            : {}),
        },
        once: true,
      })
    }
    const [first, second] = replacement.children
    const newStart = await modelScript.queue(
      {
        toolCalls: [
          qwenWorkflowToolCall(replacement.callId, [
            `export const meta = { name: ${JSON.stringify(replacement.name)}, description: "Run replacement assignments." }`,
            'phase("Replacement assignments")',
            `const first = await agent(${JSON.stringify(modelScript.prompt(first.task))}, { label: ${JSON.stringify(first.label)} })`,
            `const second = await agent(${JSON.stringify(modelScript.prompt(second.task))}, { label: ${JSON.stringify(second.label)} })`,
            'return { first, second }',
          ].join('\n')),
        ],
      },
      { text: 'Both replacement native assignments completed.' },
    )
    await sendMessage(page, modelScript.prompt('Start the replacement workflow with two native assignments.'))
    await modelScript.waitForGate(replacement.gate)
    const newRunKey = qwenRegistryRowKey(QWEN_TOOL.Workflow, newRoot.agentSessionId, replacement.callId)
    if (newRunKey === undefined)
      throw new Error('The replacement Qwen workflow has no native registry identity.')
    expect(newRunKey).not.toBe(oldRunKey)
    const newRunningGroup = await readGroup(newRunKey, [first.label], BackgroundTaskStatus.RUNNING)
    const newRunningRows = newRunningGroup.tasks.map(task => backgroundTaskRows(page, { taskId: task.id }).first())
    for (const row of newRunningRows)
      await expect(row).toHaveAttribute('data-status', 'running')
    await expectRowsInWorkflowGroup(newRunningRows, newRunningGroup.label)
    await readGroup(oldRunKey, [outgoing.label], BackgroundTaskStatus.STOPPED, stoppedGroup.label)
    for (const row of oldRows)
      await expect(row).toHaveAttribute('data-status', 'stopped')
    await expectRowsInWorkflowGroup(oldRows, stoppedGroup.label)

    await modelScript.releaseGate(replacement.gate)
    await modelScript.waitForSteps(newStart + 2)
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'Both replacement native assignments completed.' })).toHaveCount(1)
    const completion = await modelScript.requestAt(newStart + 1)
    const workflowResult = nativeToolResult(completion, replacement.callId)
    for (const child of replacement.children)
      expect(workflowResult).toContain(child.answer)
    const status = await modelScript.status()
    expect(status.ruleMatches[outgoing.gate]).toBe(1)
    for (const index of replacement.children.keys())
      expect(status.ruleMatches[`replacement-native-work-unit-${index}`]).toBe(1)
    expect(status.unexpectedRequests).toEqual([])

    for (const reload of [false, true]) {
      if (reload)
        await page.reload()
      await expandBackgroundTasksSection(page)
      const preserved = await readGroup(oldRunKey, [outgoing.label], BackgroundTaskStatus.STOPPED, stoppedGroup.label)
      expect(preserved.tasks.map(task => ({ id: task.id, groupLabel: task.groupLabel, endedAt: task.endedAt })).sort((a, b) => a.id.localeCompare(b.id)))
        .toEqual(stoppedGroup.tasks.map(task => ({ id: task.id, groupLabel: task.groupLabel, endedAt: task.endedAt })).sort((a, b) => a.id.localeCompare(b.id)))
      const finished = await readGroup(newRunKey, replacement.children.map(child => child.label), BackgroundTaskStatus.SUCCEEDED, replacement.name)
      const finishedRows = finished.tasks.map(task => backgroundTaskRows(page, { taskId: task.id }).first())
      for (const row of oldRows)
        await expect(row).toHaveAttribute('data-status', 'stopped')
      for (const row of finishedRows)
        await expect(row).toHaveAttribute('data-status', 'succeeded')
      await expectRowsInWorkflowGroup(oldRows, stoppedGroup.label)
      await expectRowsInWorkflowGroup(finishedRows, replacement.name)
    }
  }
  finally {
    await Promise.all([
      modelScript.releaseGateIfHeld(outgoing.gate),
      modelScript.releaseGateIfHeld(replacement.gate),
    ])
  }
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/520.
