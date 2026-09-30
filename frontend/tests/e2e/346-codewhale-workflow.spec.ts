import { CODEWHALE_E2E_SKIP_REASON, codewhaleTest, expect } from './codewhale-fixtures'
import { codewhaleWorkflowToolCall } from './helpers/providerToolCalls'
import { expandBackgroundTasksSection, expectRowBecomesFinal } from './helpers/subagentRegistry'
import { applyPermissionPreset, sendMessage, waitForSettingsHydrated } from './helpers/ui'
import { workflowGroupHeading } from './helpers/workflowGrouping'

codewhaleTest.skip(!!CODEWHALE_E2E_SKIP_REASON, CODEWHALE_E2E_SKIP_REASON || '')

codewhaleTest.describe('Codewhale workflow grouping', () => {
  codewhaleTest('shows one workflow row after its native child answers', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace
    await waitForSettingsHydrated(page)
    await applyPermissionPreset(page, 'bypass')

    const goal = 'Probe one read-only child'
    await modelScript.rule({
      name: 'the workflow child answers',
      when: { user: 'Reply with CODEWHALE_WORKFLOW_CHILD' },
      respond: { text: 'CODEWHALE_WORKFLOW_CHILD' },
      once: true,
    })
    await modelScript.queue({
      toolCalls: [codewhaleWorkflowToolCall('run-workflow', goal, modelScript.prompt('Reply with CODEWHALE_WORKFLOW_CHILD.'))],
    })
    await modelScript.fallback({ text: 'The workflow finished.' })
    await sendMessage(page, modelScript.prompt('Run one read-only workflow child.'))
    await modelScript.waitForSteps()

    await expandBackgroundTasksSection(page)
    const workflow = page.locator('[data-testid="bg-task-row"]:visible[data-kind="workflow"]').first()
    await expectRowBecomesFinal(page, workflow)
    await expect(workflow).toHaveAttribute('data-status', 'completed')
    expect((await modelScript.status()).ruleMatches['the workflow child answers']).toBe(1)
    await expect.poll(() => workflowGroupHeading(workflow))
      .toContain(goal)
    await expect(page.locator('[data-testid="bg-task-row"]:visible')).toHaveCount(1)
    await expect(page.locator('[data-testid="bg-task-row"]:visible[data-kind="subagent"]')).toHaveCount(0)
  })
})
