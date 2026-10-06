import { expect } from '@playwright/test'
import { escapeRegExp } from '../../../src/lib/regexp'
import { grokTest } from '../grok-fixtures'
import { grokWorkflowToolCall } from '../helpers/providerToolCalls'
import { backgroundTaskRows, expandBackgroundTasksSection, expectRowBecomesFinal } from '../helpers/subagentRegistry'
import { assistantBubbles, openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expectRowsInWorkflowGroup } from '../helpers/workflowGrouping'
import { openProviderAgent } from '../helpers/workspace'
import { grokChildTurn } from './childScenario'
import { GROK_AGENT } from './scenarios'

grokTest.describe('Grok Build workflow grouping', () => {
  grokTest('groups a native Rhai workflow with its child', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, GROK_AGENT, { optionValues: { approvalMode: 'always-approve' } })
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
      when: grokChildTurn('Reply with GROK_WORKFLOW_CHILD'),
      respond: { text: 'GROK_WORKFLOW_CHILD' },
      once: true,
    })
    const start = await modelScript.queue(
      { toolCalls: [grokWorkflowToolCall('run-workflow', script)] },
      { text: 'The workflow finished.' },
    )
    await modelScript.fallback({ text: 'The workflow notification arrived.' })
    await sendMessage(page, modelScript.prompt('Run the native workflow with one child.'))
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'The workflow finished.' }).first()).toBeVisible()

    await expandBackgroundTasksSection(page)
    const workflow = backgroundTaskRows(page, { kind: 'workflow' }).first()
    await expectRowBecomesFinal(page, workflow)
    await expect(workflow).toHaveAttribute('data-status', 'completed')
    expect((await modelScript.status()).ruleMatches['the workflow child answers']).toBe(1)

    const child = backgroundTaskRows(page, { kind: 'subagent' }).filter({ hasText: 'Probe child' }).first()
    await expectRowBecomesFinal(page, child)
    // No spec states the full heading text, so the pattern requires only the workflow name inside it.
    await expectRowsInWorkflowGroup([workflow, child], new RegExp(escapeRegExp(name)))
  })
})
