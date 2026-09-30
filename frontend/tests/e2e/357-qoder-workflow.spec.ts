import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { qoderWorkflowToolCall, readToolCall } from './helpers/providerToolCalls'
import { expandBackgroundTasksSection, expectRowBecomesFinal, openChildTabFromRow } from './helpers/subagentRegistry'
import { assistantBubbles, sendMessage, tabById, userBubbles, visibleOnly } from './helpers/ui'
import { workflowGroupHeading, workflowRowsShareGroup } from './helpers/workflowGrouping'
import { expect, QODER_E2E_SKIP_REASON, qoderTest } from './qoder-fixtures'

qoderTest.skip(!!QODER_E2E_SKIP_REASON, QODER_E2E_SKIP_REASON || '')

const WORKFLOW_NAME = 'qoder-e2e-workflow'
const FIRST_ANSWER = 'QODER_WORKFLOW_CHILD'
const SECOND_ANSWER = 'QODER_SECOND_CHILD'
const FILE_MARKER = 'QODER_WORKFLOW_FILE_MARKER'

qoderTest('groups a native Workflow run with two saved child transcripts', async ({ qoderWorkspace, page, modelScript }) => {
  const file = 'qoder-workflow-note.txt'
  writeFileSync(join(qoderWorkspace.workingDir, file), FILE_MARKER)
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
    when: { system: 'You are a subagent spawned by a workflow orchestration script', user: firstPrompt },
    respond: { toolCalls: [readToolCall(AgentProvider.QODER, 'workflow-read', file)] },
    once: true,
  }, {
    name: 'the first workflow child answers after the read',
    when: { system: 'You are a subagent spawned by a workflow orchestration script', body: FILE_MARKER },
    respond: { text: FIRST_ANSWER, gate },
    once: true,
  }, {
    name: 'the second workflow child answers',
    when: { system: 'You are a subagent spawned by a workflow orchestration script', user: secondPrompt },
    respond: { text: SECOND_ANSWER },
    once: true,
  })
  await modelScript.queue(
    { toolCalls: [qoderWorkflowToolCall('run-qoder-workflow', script)] },
    { text: 'The Qoder workflow started.' },
  )
  await modelScript.fallback({ text: 'The Qoder workflow result arrived.' })

  const parentTabId = await page.locator('[data-testid="tab"][data-tab-type="agent"]').first().getAttribute('data-tab-id') ?? ''
  expect(parentTabId).not.toBe('')
  await sendMessage(page, modelScript.prompt('Run the two-child workflow and report its result.'))
  await modelScript.waitForSteps(1)
  const permission = page.locator('[data-testid="control-banner"]:visible')
  await expect(permission).toContainText('Workflow')
  await page.getByTestId('control-allow-btn').filter({ visible: true }).click()
  await modelScript.waitForGate(gate)

  const workflow = page.locator('[data-testid="bg-task-row"]:visible[data-kind="workflow"]').first()
  const firstChild = page.locator('[data-testid="bg-task-row"]:visible[data-kind="subagent"]').filter({ hasText: `Read ${file}` }).first()
  let firstChildTabId = ''
  try {
    await expandBackgroundTasksSection(page)
    await expect(workflow).toBeVisible()
    await expect(workflow).toHaveAttribute('data-status', 'running')
    await expect(firstChild).toHaveAttribute('data-status', 'running')
    firstChildTabId = await openChildTabFromRow(page, firstChild)
    await expect(userBubbles(page).filter({ hasText: `Read ${file}` }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: FIRST_ANSWER })).toHaveCount(0)
  }
  finally {
    try {
      await modelScript.releaseGate(gate)
    }
    finally {
      await tabById(page, parentTabId).click()
    }
  }

  await modelScript.waitForSteps()
  await expect.poll(async () => (await modelScript.status()).ruleMatches['the second workflow child answers'] ?? 0).toBe(1)
  await expect(assistantBubbles(page).filter({ hasText: 'The Qoder workflow result arrived.' }).first()).toBeVisible()
  await expectRowBecomesFinal(page, workflow)
  await expect(workflow).toHaveAttribute('data-status', 'completed')
  const secondChild = page.locator('[data-testid="bg-task-row"]:visible[data-kind="subagent"]').filter({ hasText: `Reply with ${SECOND_ANSWER}` }).first()
  await expect(firstChild).toHaveAttribute('data-status', 'completed')
  await expect(secondChild).toHaveAttribute('data-status', 'completed')
  await expect.poll(() => workflowGroupHeading(firstChild)).toBe(WORKFLOW_NAME)
  await expect.poll(() => workflowGroupHeading(secondChild)).toBe(WORKFLOW_NAME)
  await expect.poll(() => workflowRowsShareGroup(workflow, firstChild)).toBe(true)
  await expect.poll(() => workflowRowsShareGroup(workflow, secondChild)).toBe(true)
  await expect.poll(async () => await firstChild.getAttribute('data-child-agent-id') ?? '').not.toBe('')
  await expect.poll(async () => await secondChild.getAttribute('data-child-agent-id') ?? '').not.toBe('')
  await tabById(page, firstChildTabId).click()
  await expect(visibleOnly(page.getByText(FILE_MARKER, { exact: false })).first()).toBeVisible()
  await expect(assistantBubbles(page).filter({ hasText: FIRST_ANSWER }).first()).toBeVisible()
  await tabById(page, parentTabId).click()
  await openChildTabFromRow(page, secondChild)
  await expect(userBubbles(page).filter({ hasText: `Reply with ${SECOND_ANSWER}` }).first()).toBeVisible()
  await expect(assistantBubbles(page).filter({ hasText: SECOND_ANSWER }).first()).toBeVisible()
  await expect(assistantBubbles(page).filter({ hasText: FIRST_ANSWER })).toHaveCount(0)
})
