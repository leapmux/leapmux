import { zcodeCreateWorkflowToolCall, zcodeWorkflowSkillToolCall } from './helpers/providerToolCalls'
import { sendMessage } from './helpers/ui'
import { workflowGroupHeading } from './helpers/workflowGrouping'
import { expect, ZCODE_E2E_SKIP_REASON, zcodeTest } from './zcode-fixtures'

const WORKFLOW_NAME = 'leapmux-e2e-probe'
const SCRIPT = 'phase("Compute a local answer"); return { conclusion: "ZCODE_WORKFLOW_DONE" };'

zcodeTest.describe('ZCode Workflow grouping', () => {
  zcodeTest.skip(!!ZCODE_E2E_SKIP_REASON, ZCODE_E2E_SKIP_REASON || '')
  zcodeTest('shows a native CreateWorkflow run without grouped work', async ({ authenticatedZCodeWorkspace, page, modelScript }) => {
    void authenticatedZCodeWorkspace
    await modelScript.fallback({ text: 'Workflow notification noted.' })
    await modelScript.queue(
      { toolCalls: [zcodeWorkflowSkillToolCall('load-workflow-skill')] },
      { toolCalls: [zcodeCreateWorkflowToolCall('create-workflow', WORKFLOW_NAME, SCRIPT)] },
      { text: 'The workflow launched.' },
    )
    await sendMessage(page, modelScript.prompt('Use a workflow to compute one local answer.'))
    await modelScript.waitForSteps(2)

    const permission = page.getByTestId('control-banner').filter({ visible: true })
    await expect(permission).toContainText('CreateWorkflow')
    await expect(permission).toContainText(WORKFLOW_NAME)
    await page.getByTestId('control-actions').getByRole('button', { name: 'Allow', exact: true }).click()
    await modelScript.waitForSteps(3)
    await expect(permission).toHaveCount(0)
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
})
