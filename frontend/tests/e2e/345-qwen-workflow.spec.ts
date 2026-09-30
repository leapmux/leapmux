import { OPTION_ID_PERMISSION_MODE } from '../../src/components/chat/settingsGroups'
import { qwenWorkflowToolCall } from './helpers/providerToolCalls'
import { expandBackgroundTasksSection, expectRowBecomesFinal } from './helpers/subagentRegistry'
import { assistantBubbles, openWorkspace, sendMessage, waitForAgentIdle } from './helpers/ui'
import { workflowGroupHeading } from './helpers/workflowGrouping'
import { expect, openQwenAgent, QWEN_E2E_SKIP_REASON, qwenTest } from './qwen-fixtures'

qwenTest.skip(!!QWEN_E2E_SKIP_REASON, QWEN_E2E_SKIP_REASON || '')

qwenTest.describe('Qwen Code workflow grouping', () => {
  qwenTest('shows one workflow row after its native child answers', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
    await openQwenAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { [OPTION_ID_PERMISSION_MODE]: 'yolo' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)

    const childPrompt = modelScript.prompt('Reply with QWEN_WORKFLOW_CHILD.')
    const script = [
      'export const meta = { name: "qwen-e2e-workflow", description: "Ask one child." }',
      'phase("Probe")',
      `const answer = await agent(${JSON.stringify(childPrompt)}, { label: "Probe child" })`,
      'return answer',
    ].join('\n')
    await modelScript.rule({
      name: 'the workflow child answers',
      when: { user: 'Reply with QWEN_WORKFLOW_CHILD' },
      respond: { text: 'QWEN_WORKFLOW_CHILD' },
      once: true,
    })
    await modelScript.queue(
      { toolCalls: [qwenWorkflowToolCall('run-workflow', script)] },
      { text: 'The workflow finished.' },
    )
    await sendMessage(page, modelScript.prompt('Run one workflow child and report completion.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    expect((await modelScript.status()).ruleMatches['the workflow child answers']).toBe(1)
    await expect(assistantBubbles(page).filter({ hasText: 'The workflow finished.' }).first()).toBeVisible()

    await expandBackgroundTasksSection(page)
    const workflow = page.locator('[data-testid="bg-task-row"]:visible[data-kind="workflow"]').first()
    await expectRowBecomesFinal(page, workflow)
    await expect(workflow).toHaveAttribute('data-status', 'completed')
    await expect.poll(() => workflowGroupHeading(workflow))
      .toBe('Workflow')
    await expect(page.locator('[data-testid="bg-task-row"]:visible')).toHaveCount(1)
    await expect(page.locator('[data-testid="bg-task-row"]:visible[data-kind="subagent"]')).toHaveCount(0)
  })
})
