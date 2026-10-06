import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { withCleanup } from '../helpers/cleanup'
import { selectedAgentTabId } from '../helpers/nativeScenario'
import { qoderWorkflowToolCall, readToolCall } from '../helpers/providerToolCalls'
import { expandBackgroundTasksSection, expectRowBecomesFinal, openChildTabFromRow } from '../helpers/subagentRegistry'
import { answerControl, assistantBubbles, sendMessage, tabById, userBubbles, visibleOnly, waitForControlBanner } from '../helpers/ui'
import { expectRowsInWorkflowGroup } from '../helpers/workflowGrouping'
import { expect, qoderTest } from '../qoder-fixtures'

qoderTest.describe('native workflow grouping', () => {
  const WORKFLOW_NAME = 'qoder-e2e-workflow'

  const FIRST_ANSWER = 'QODER_WORKFLOW_CHILD'

  const SECOND_ANSWER = 'QODER_SECOND_CHILD'

  const FILE_MARKER = 'QODER_WORKFLOW_FILE_MARKER'

  /** The system prompt of a child that a Qoder workflow script spawns. */
  const WORKFLOW_CHILD_SYSTEM = 'You are a subagent spawned by a workflow orchestration script'

  qoderTest('groups a native Workflow run with two saved child transcripts', async ({ native, authenticatedQoderWorkspace }) => {
    const { page, modelScript } = native
    const file = 'qoder-workflow-note.txt'
    writeFileSync(join(authenticatedQoderWorkspace.workingDir, file), FILE_MARKER)
    const firstPrompt = modelScript.prompt(`Read ${file} and report its marker.`)
    const secondPrompt = modelScript.prompt(`Reply with ${SECOND_ANSWER}.`)
    const script = [
      `export const meta = { name: ${JSON.stringify(WORKFLOW_NAME)}, description: 'Ask two children.', phases: [{ title: 'Probe' }] };`,
      `const first = await agent(${JSON.stringify(firstPrompt)}, { label: 'Probe child' });`,
      `const second = await agent(${JSON.stringify(secondPrompt)}, { label: 'Second child' });`,
      'return { first, second };',
    ].join('\n')
    const gate = 'qoder-workflow-first-answer'
    await modelScript.rule({
      name: 'the first workflow child reads its file',
      when: { system: WORKFLOW_CHILD_SYSTEM, user: firstPrompt },
      respond: { toolCalls: [readToolCall(native.provider, 'workflow-read', file)] },
      once: true,
    }, {
      name: 'the first workflow child answers after the read',
      when: { system: WORKFLOW_CHILD_SYSTEM, body: FILE_MARKER },
      respond: { text: FIRST_ANSWER, gate },
      once: true,
    }, {
      name: 'the second workflow child answers',
      when: { system: WORKFLOW_CHILD_SYSTEM, user: secondPrompt },
      respond: { text: SECOND_ANSWER },
      once: true,
    })
    const start = await modelScript.queue(
      { toolCalls: [qoderWorkflowToolCall('run-qoder-workflow', script)] },
      { text: 'The Qoder workflow started.' },
    )
    await modelScript.fallback({ text: 'The Qoder workflow result arrived.' })

    const parentTabId = await selectedAgentTabId(page)
    await sendMessage(page, modelScript.prompt('Run the two-child workflow and report its result.'))
    await modelScript.waitForSteps(start + 1)
    await expect(await waitForControlBanner(page)).toContainText('Workflow')
    await answerControl(page, 'allow')
    await modelScript.waitForGate(gate)

    const workflow = page.locator('[data-testid="bg-task-row"]:visible[data-kind="workflow"]').first()
    const firstChild = page.locator('[data-testid="bg-task-row"]:visible[data-kind="subagent"]').filter({ hasText: `Read ${file}` }).first()
    const firstChildTabId = await withCleanup(async () => {
      await expandBackgroundTasksSection(page)
      await expect(workflow).toBeVisible()
      await expect(workflow).toHaveAttribute('data-status', 'running')
      await expect(firstChild).toHaveAttribute('data-status', 'running')
      const childTabId = await openChildTabFromRow(page, firstChild)
      await expect(userBubbles(page).filter({ hasText: `Read ${file}` }).first()).toBeVisible()
      await expect(assistantBubbles(page).filter({ hasText: FIRST_ANSWER })).toHaveCount(0)
      return childTabId
    }, async () => {
      try {
        await modelScript.releaseGate(gate)
      }
      finally {
        await tabById(page, parentTabId).click()
      }
    })

    await modelScript.waitForSteps(start + 2)
    await expect.poll(async () => (await modelScript.status()).ruleMatches['the second workflow child answers'] ?? 0).toBe(1)
    await expect(assistantBubbles(page).filter({ hasText: 'The Qoder workflow result arrived.' }).first()).toBeVisible()
    await expectRowBecomesFinal(page, workflow)
    await expect(workflow).toHaveAttribute('data-status', 'completed')
    const secondChild = page.locator('[data-testid="bg-task-row"]:visible[data-kind="subagent"]').filter({ hasText: `Reply with ${SECOND_ANSWER}` }).first()
    await expect(firstChild).toHaveAttribute('data-status', 'completed')
    await expect(secondChild).toHaveAttribute('data-status', 'completed')
    await expectRowsInWorkflowGroup([workflow, firstChild, secondChild], WORKFLOW_NAME)
    // The completed first child keeps the link to its exact transcript. `openChildTabFromRow` below proves the link
    // of the second.
    await expect(firstChild).toHaveAttribute('data-child-agent-id', firstChildTabId)
    await tabById(page, firstChildTabId).click()
    await expect(visibleOnly(page.getByText(FILE_MARKER, { exact: false })).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: FIRST_ANSWER }).first()).toBeVisible()
    await tabById(page, parentTabId).click()
    await openChildTabFromRow(page, secondChild)
    await expect(userBubbles(page).filter({ hasText: `Reply with ${SECOND_ANSWER}` }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: SECOND_ANSWER }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: FIRST_ANSWER })).toHaveCount(0)
  })
})
