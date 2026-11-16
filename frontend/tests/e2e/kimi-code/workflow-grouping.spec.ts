import { expect } from '@playwright/test'
import { BackgroundTaskKind, BackgroundTaskRole, BackgroundTaskStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { escapeRegExp } from '../../../src/lib/regexp'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { kimiAgentSwarmToolCall } from '../helpers/providerToolCalls'
import { backgroundTaskRows, expandBackgroundTasksSection, expectRowBecomesFinal, openChildTabFromRow } from '../helpers/subagentRegistry'
import { assistantBubbles, chooseSettingsOption, sendMessage, tabById, waitForAgentIdle, waitForSettingsIdle } from '../helpers/ui'
import { expectRowsInWorkflowGroup } from '../helpers/workflowGrouping'
import { kimiTest } from '../kimi-fixtures'
import { kimiChildTurn, prepareKimiChildRun } from './childScenario'

kimiTest.describe('Kimi Code workflow grouping', () => {
  kimiTest('groups native AgentSwarm members under its description', async ({ native }) => {
    const { page, modelScript } = native
    await prepareKimiChildRun(page)
    await chooseSettingsOption(page, 'swarmMode-on')
    await waitForSettingsIdle(page)

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
      {
        toolCalls: [
          kimiAgentSwarmToolCall('swarm-run', description, modelScript.prompt('Reply with exactly {{item}}.'), ['SWARM_ALPHA', 'SWARM_BRAVO']),
        ],
      },
      { text: 'The swarm finished.' },
    )
    await sendMessage(page, modelScript.prompt('Run one native AgentSwarm member and report completion.'))
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'The swarm finished.' }).first()).toBeVisible()
    const status = await modelScript.status()
    expect(status.ruleMatches['the first swarm member answers']).toBe(1)
    expect(status.ruleMatches['the second swarm member answers']).toBe(1)

    const parentId = (await currentNativeAgent(native)).id
    await expandBackgroundTasksSection(page)
    const proveOutcomes = async () => {
      const rows = (await readNativeSidebarSnapshot(native)).backgroundTasks
      const outcomes = rows.filter(row => row.kind === BackgroundTaskKind.WORKFLOW
        && row.role === BackgroundTaskRole.TASK_OUTCOME)
      expect(outcomes).toHaveLength(4)
      expect(outcomes.every(row => row.status === BackgroundTaskStatus.SUCCEEDED)).toBe(true)
      const childIds = new Set(outcomes.map(row => row.childAgentId))
      expect(childIds.size).toBe(2)
      expect(childIds.has('')).toBe(false)
      for (const childId of childIds) {
        expect(outcomes.filter(row => row.childAgentId === childId)).toHaveLength(2)
        const activity = rows.filter(row => row.role === BackgroundTaskRole.CHILD_ACTIVITY
          && row.childAgentId === childId)
        expect(activity).toHaveLength(1)
        expect(activity[0]?.status).not.toBe(BackgroundTaskStatus.RUNNING)
      }
      const grouped = outcomes.filter(row => row.groupKey !== '')
      expect(grouped).toHaveLength(2)
      expect(new Set(grouped.map(row => row.groupKey)).size).toBe(1)
      expect(grouped.every(row => row.groupLabel === description)).toBe(true)
      const members = grouped.map(row => backgroundTaskRows(page, { kind: 'workflow', taskId: row.id }).first())
      for (const member of members) {
        await expectRowBecomesFinal(page, member)
        await expect(member).toHaveAttribute('data-status', 'succeeded')
      }
      await expectRowsInWorkflowGroup(members, new RegExp(escapeRegExp(description)))
      for (const outcome of outcomes) {
        const row = backgroundTaskRows(page, { kind: 'workflow', taskId: outcome.id }).first()
        expect(await openChildTabFromRow(page, row)).toBe(outcome.childAgentId)
        await tabById(page, parentId).click()
      }
      return outcomes.map(row => row.id).sort()
    }
    const identities = await proveOutcomes()
    await page.reload()
    await waitForAgentIdle(page)
    await expandBackgroundTasksSection(page)
    expect(await proveOutcomes()).toEqual(identities)
  })
})
