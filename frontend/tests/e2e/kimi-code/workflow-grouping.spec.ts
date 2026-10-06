import { expect } from '@playwright/test'
import { escapeRegExp } from '../../../src/lib/regexp'
import { kimiAgentSwarmToolCall } from '../helpers/providerToolCalls'
import { expandBackgroundTasksSection, expectRowBecomesFinal } from '../helpers/subagentRegistry'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expectRowsInWorkflowGroup } from '../helpers/workflowGrouping'
import { kimiTest } from '../kimi-fixtures'
import { kimiChildTurn, prepareKimiChildRun } from './childScenario'

kimiTest.describe('Kimi Code workflow grouping', () => {
  kimiTest('groups native AgentSwarm members under its description', async ({ native }) => {
    const { page, modelScript } = native
    await prepareKimiChildRun(page)

    const description = 'Review the probe items'
    await modelScript.rule(
      {
        name: 'the first swarm member answers',
        when: kimiChildTurn('Reply with exactly SWARM_ALPHA'),
        respond: { text: 'SWARM_ALPHA' },
        once: true,
      },
      {
        name: 'the second swarm member answers',
        when: kimiChildTurn('Reply with exactly SWARM_BRAVO'),
        respond: { text: 'SWARM_BRAVO' },
        once: true,
      },
    )
    const start = await modelScript.queue(
      { toolCalls: [kimiAgentSwarmToolCall('swarm-run', description, modelScript.prompt('Reply with exactly {{item}}.'), ['SWARM_ALPHA', 'SWARM_BRAVO'])] },
      { text: 'The swarm finished.' },
    )
    await sendMessage(page, modelScript.prompt('Run one native AgentSwarm member and report completion.'))
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'The swarm finished.' }).first()).toBeVisible()
    const status = await modelScript.status()
    expect(status.ruleMatches['the first swarm member answers']).toBe(1)
    expect(status.ruleMatches['the second swarm member answers']).toBe(1)

    await expandBackgroundTasksSection(page)
    const members = page.locator('[data-testid="bg-task-row"]:visible[data-kind="workflow"]')
    await expect(members).toHaveCount(2)
    for (const member of await members.all()) {
      await expectRowBecomesFinal(page, member)
      await expect(member).toHaveAttribute('data-status', 'completed')
    }
    // No spec states the full heading text, so the pattern requires only the swarm description inside it.
    await expectRowsInWorkflowGroup([members.nth(0), members.nth(1)], new RegExp(escapeRegExp(description)))
  })
})
