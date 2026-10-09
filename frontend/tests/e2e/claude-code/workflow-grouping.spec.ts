import { expect } from '@playwright/test'
import { claudeTest } from '../claude-fixtures'
import { claudeWorkflowToolCall } from '../helpers/providerToolCalls'
import { backgroundTaskRows, expandBackgroundTasksSection, expectRowBecomesFinal } from '../helpers/subagentRegistry'
import { agentTabs, answerControl, assistantBubbles, controlButton, expectAgentTabCount, sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'
import { workflowGroupHeading } from '../helpers/workflowGrouping'

const WORKFLOW_NAME = 'leapmux-e2e-probe'

const CHILD_RESULT = 'WORKFLOW_CHILD_OK'

function workflowScript(prompt: string): string {
  return [
    `export const meta = { name: ${JSON.stringify(WORKFLOW_NAME)}, description: 'Run one local probe', phases: [{ title: 'Probe' }] };`,
    `const result = await agent(${JSON.stringify(prompt)}, { label: 'probe', phase: 'Probe' });`,
    'return { result };',
  ].join('\n')
}

claudeTest('shows a native Claude Workflow run without a grouped child row', async ({ authenticatedWorkspace, page, modelScript }) => {
  void authenticatedWorkspace
  const tabsBefore = await agentTabs(page).count()
  const childPrompt = modelScript.prompt(`Reply with ${CHILD_RESULT}.`)
  await modelScript.rule({
    name: 'the workflow child answers locally',
    // Claude puts system reminders before the child's harness text. The root's
    // Workflow result quotes the prompt but does not carry this harness text.
    when: { user: ['\\[Workflow harness — computed task\\]', CHILD_RESULT] },
    respond: { text: CHILD_RESULT },
  })
  // Claude can start more root turns when the background workflow reports its
  // result. Their count depends on when the workflow notification arrives.
  await modelScript.fallback({ text: 'Workflow notification noted.' })
  const start = await modelScript.queue(
    { toolCalls: [claudeWorkflowToolCall('workflow-probe', workflowScript(childPrompt))] },
    { text: 'The workflow finished.' },
  )
  await sendMessage(page, modelScript.prompt('Run the one-child workflow and report its result.'))
  await modelScript.waitForSteps(start + 1)
  const permission = await waitForControlBanner(page)
  await expect(permission).toContainText('Permission Required')
  await expect(permission).toContainText(WORKFLOW_NAME)
  // A label other than `Allow` states a remembered allow, so the exact label proves an answer for this request only.
  await expect(controlButton(page, 'allow')).toHaveText('Allow')
  await answerControl(page, 'allow')
  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)

  await expect(assistantBubbles(page).filter({ hasText: 'The workflow finished.' }).first()).toBeVisible()
  await expandBackgroundTasksSection(page)
  const workflowRows = backgroundTaskRows(page, { kind: 'workflow' })
  const row = workflowRows.filter({ hasText: 'Run one local probe' }).first()
  await expect(row).toBeVisible()
  await expectRowBecomesFinal(page, row)
  await expect(row).toHaveAttribute('data-status', 'succeeded')
  await expect.poll(() => workflowGroupHeading(row)).toContain(WORKFLOW_NAME)
  expect((await modelScript.status()).ruleMatches['the workflow child answers locally']).toBe(1)
  await expect(workflowRows).toHaveCount(1)
  await expect(backgroundTaskRows(page, { kind: 'subagent' })).toHaveCount(0)
  await expect(row).toHaveAttribute('data-child-agent-id', '')
  await expectAgentTabCount(page, tabsBefore)
})
