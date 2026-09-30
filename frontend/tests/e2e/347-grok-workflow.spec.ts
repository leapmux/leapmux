import { expect, GROK_E2E_SKIP_REASON, grokTest, openGrokAgent } from './grok-fixtures'
import { grokWorkflowToolCall } from './helpers/providerToolCalls'
import { expandBackgroundTasksSection, expectRowBecomesFinal } from './helpers/subagentRegistry'
import { assistantBubbles, openWorkspace, sendMessage, waitForAgentIdle } from './helpers/ui'
import { workflowGroupHeading, workflowRowsShareGroup } from './helpers/workflowGrouping'

grokTest.skip(!!GROK_E2E_SKIP_REASON, GROK_E2E_SKIP_REASON || '')

grokTest.describe('Grok Build workflow grouping', () => {
  grokTest('groups a native Rhai workflow with its child', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
    await openGrokAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { approvalMode: 'always-approve' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)

    const name = 'leapmux-probe'
    const childPrompt = modelScript.prompt('Reply with GROK_WORKFLOW_CHILD.')
    const script = [
      `let meta = #{ name: ${JSON.stringify(name)}, description: "Ask one child." };`,
      'phase("Probe");',
      `let answer = agent(${JSON.stringify(childPrompt)}, #{ label: "Probe child", capability_mode: "read-only" });`,
      'answer',
    ].join('\n')
    await modelScript.rule({
      name: 'the workflow child answers',
      when: { system: 'You are a Grok Build subagent\\b', user: 'Reply with GROK_WORKFLOW_CHILD' },
      respond: { text: 'GROK_WORKFLOW_CHILD' },
      once: true,
    })
    await modelScript.queue(
      { toolCalls: [grokWorkflowToolCall('run-workflow', script)] },
      { text: 'The workflow finished.' },
    )
    await modelScript.fallback({ text: 'The workflow notification arrived.' })
    await sendMessage(page, modelScript.prompt('Run the native workflow with one child.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'The workflow finished.' }).first()).toBeVisible()

    await expandBackgroundTasksSection(page)
    const workflow = page.locator('[data-testid="bg-task-row"]:visible[data-kind="workflow"]').first()
    await expectRowBecomesFinal(page, workflow)
    await expect(workflow).toHaveAttribute('data-status', 'completed')
    await expect.poll(() => workflowGroupHeading(workflow)).toContain(name)
    expect((await modelScript.status()).ruleMatches['the workflow child answers']).toBe(1)

    const child = page.locator('[data-testid="bg-task-row"]:visible[data-kind="subagent"]').filter({ hasText: 'Probe child' }).first()
    await expectRowBecomesFinal(page, child)
    await expect.poll(() => workflowGroupHeading(child)).toContain(name)
    await expect.poll(() => workflowRowsShareGroup(workflow, child)).toBe(true)
  })
})
