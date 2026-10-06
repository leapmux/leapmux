import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { diracTest } from '../dirac-fixtures'
import { diracRespondToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { backgroundTaskRows, expandBackgroundTasksSection, openChildTabFromRow } from '../helpers/subagentRegistry'
import { assistantBubbles, expectRowsInOrder, openWorkspace, sendMessage, tabById, toolRows, userBubbles, visibleOnly, waitForAgentIdle } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { DIRAC_AGENT } from './scenarios'

diracTest.describe('Dirac subagent transcript', () => {
  const PROVIDER = AgentProvider.DIRAC

  const CHILD_TASK = 'Count the files and report one number.'

  diracTest('opens each child transcript in its own tab', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, DIRAC_AGENT)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const childPrompt = modelScript.prompt(CHILD_TASK)
    const childAnswer = modelScript.prompt('DIRAC_CHILD_DONE')

    await modelScript.rule(
      {
        name: 'the Dirac child reports its count',
        when: { body: CHILD_TASK },
        respond: {
          text: 'DIRAC_CHILD_ARCHIVE_ONLY',
          toolCalls: [diracRespondToolCall('dirac-child-done', 'complete', childAnswer)],
          gate: 'dirac-child-reply',
        },
        once: true,
      },
      {
        name: 'the Dirac parent completes after the child',
        when: { body: 'Subagent results:' },
        respond: { toolCalls: [diracRespondToolCall('dirac-root-done', 'complete', 'DIRAC_ROOT_DONE')] },
        once: true,
      },
    )
    const start = await modelScript.queue({ toolCalls: [spawnSubagentToolCall(PROVIDER, 'dirac-spawn', { description: 'Count files', prompt: childPrompt })] })
    await sendMessage(page, modelScript.prompt('Delegate the count, then report.'))
    await modelScript.waitForGate('dirac-child-reply')

    await expandBackgroundTasksSection(page)
    const child = backgroundTaskRows(page, { kind: 'subagent' }).filter({ hasText: 'Count files' }).first()
    await expect(child).toBeVisible()
    // `openChildTabFromRow` waits until the row links a child agent.
    await openChildTabFromRow(page, child)
    await expect(userBubbles(page).filter({ hasText: childPrompt }).first()).toBeVisible()

    await modelScript.releaseGate('dirac-child-reply')
    await modelScript.waitForSteps(start + 1)
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'DIRAC_CHILD_DONE' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'DIRAC_CHILD_ARCHIVE_ONLY' }).first()).toBeVisible()
    await expect(toolRows(page).filter({ hasText: 'DIRAC_CHILD_DONE' }).first()).toBeVisible()
    await expectRowsInOrder(assistantBubbles(page), ['DIRAC_CHILD_ARCHIVE_ONLY', 'DIRAC_CHILD_DONE'])
    expect((await modelScript.status()).ruleMatches['the Dirac child reports its count']).toBe(1)
  })

  diracTest('opens a new child tab when a cleared session reuses the native call id', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { agentId } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, DIRAC_AGENT)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const runs = [
      { task: 'FIRSTCHILDTASK count the first set.', description: 'First child count', archive: 'DIRACFIRSTARCHIVE', report: 'DIRACFIRSTREPORT', root: 'DIRACFIRSTROOT' },
      { task: 'SECONDCHILDTASK count the second set.', description: 'Second child count', archive: 'DIRACSECONDARCHIVE', report: 'DIRACSECONDREPORT', root: 'DIRACSECONDROOT' },
    ] as const
    let previousChildID = ''
    let previousPrompt = ''
    let previousArchive = ''

    for (const [index, run] of runs.entries()) {
      if (index > 0) {
        await sendMessage(page, '/clear')
        await expect(visibleOnly(page.getByText('Context cleared'))).toBeVisible()
      }
      const childPrompt = modelScript.prompt(run.task)
      await modelScript.rule(
        {
          name: `dirac-${index}-child`,
          when: { body: run.task },
          respond: {
            text: run.archive,
            toolCalls: [diracRespondToolCall(`dirac-${index}-child-done`, 'complete', modelScript.prompt(run.report))],
          },
          once: true,
        },
        {
          name: `dirac-${index}-parent`,
          when: { body: 'Subagent results:' },
          respond: { toolCalls: [diracRespondToolCall(`dirac-${index}-root-done`, 'complete', run.root)] },
          once: true,
        },
      )
      const start = await modelScript.queue({
        toolCalls: [spawnSubagentToolCall(PROVIDER, 'dirac-reused-child-call', { description: run.description, prompt: childPrompt })],
      })
      await sendMessage(page, modelScript.prompt(`Delegate ${run.description}.`))
      await modelScript.waitForSteps(start + 1)
      await waitForAgentIdle(page)

      await expandBackgroundTasksSection(page)
      const row = backgroundTaskRows(page, { kind: 'subagent' }).filter({ hasText: run.description }).first()
      await expect(row).toHaveAttribute('data-status', 'completed')
      // `openChildTabFromRow` requires the row to link a child agent, and returns that agent.
      const childID = await openChildTabFromRow(page, row)
      if (previousChildID)
        expect(childID).not.toBe(previousChildID)
      await expect(userBubbles(page).filter({ hasText: childPrompt }).first()).toBeVisible()
      await expect(assistantBubbles(page).filter({ hasText: run.archive }).first()).toBeVisible()
      await expect(assistantBubbles(page).filter({ hasText: run.report }).first()).toBeVisible()
      if (previousChildID) {
        await expect(userBubbles(page).filter({ hasText: previousPrompt })).toHaveCount(0)
        await expect(assistantBubbles(page).filter({ hasText: previousArchive })).toHaveCount(0)
      }
      await expectRowsInOrder(assistantBubbles(page), [run.archive, run.report])
      const status = await modelScript.status()
      expect(status.ruleMatches[`dirac-${index}-child`]).toBe(1)
      expect(status.ruleMatches[`dirac-${index}-parent`]).toBe(1)
      previousChildID = childID
      previousPrompt = childPrompt
      previousArchive = run.archive
      await tabById(page, agentId).click()
    }
  })
})
