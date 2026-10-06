import { expect } from '@playwright/test'
import { zcodeCreateWorkflowToolCall, zcodeWorkflowSkillToolCall } from '../helpers/providerToolCalls'
import { answerControl, controlButton, expectNoControlBanner, sendMessage, waitForControlBanner } from '../helpers/ui'
import { workflowGroupHeading } from '../helpers/workflowGrouping'
import { zcodeTest } from '../zcode-fixtures'

const WORKFLOW_NAME = 'leapmux-e2e-probe'

const SCRIPT = 'phase("Compute a local answer"); return { conclusion: "ZCODE_WORKFLOW_DONE" };'

zcodeTest('shows a native CreateWorkflow run without grouped work', async ({ native }) => {
  const { page, modelScript } = native
  await modelScript.fallback({ text: 'Workflow notification noted.' })
  const start = await modelScript.queue(
    { toolCalls: [zcodeWorkflowSkillToolCall('load-workflow-skill')] },
    { toolCalls: [zcodeCreateWorkflowToolCall('create-workflow', WORKFLOW_NAME, SCRIPT)] },
    { text: 'The workflow launched.' },
  )
  await sendMessage(page, modelScript.prompt('Use a workflow to compute one local answer.'))
  await modelScript.waitForSteps(start + 2)

  const permission = await waitForControlBanner(page)
  await expect(permission).toContainText('CreateWorkflow')
  await expect(permission).toContainText(WORKFLOW_NAME)
  // A label other than `Allow` states a remembered allow, so the exact label proves an answer for this request only.
  await expect(controlButton(page, 'allow')).toHaveText('Allow')
  await answerControl(page, 'allow')
  await modelScript.waitForSteps(start + 3)
  await expectNoControlBanner(page)
  const workflowFilter = page.getByTestId('bg-task-filter-workflow').filter({ visible: true }).first()
  await expect(workflowFilter).toBeVisible()
  await workflowFilter.click()
  await expect(workflowFilter).toHaveAttribute('aria-selected', 'true')
  const row = page.locator('[data-testid="bg-task-row"]:visible[data-kind="workflow"]').filter({ hasText: WORKFLOW_NAME }).first()
  await expect(row).toBeVisible()
  await expect(row).toHaveAttribute('data-status', /^(running|completed)$/)
  await page.getByTestId('bg-task-filter-all').filter({ visible: true }).first().click()
  await expect(page.locator('[data-testid="bg-task-row"]:visible')).toHaveCount(1)
  await expect.poll(() => workflowGroupHeading(row)).toBe('')
})
