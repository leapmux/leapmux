import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { clineTest, offeredTools } from '../cline-fixtures'
import { stepRequest } from '../helpers/mockModelScript'
import { clineRunTeammateTaskToolCall, clineSpawnTeammateToolCall } from '../helpers/providerToolCalls'
import { expandBackgroundTasksSection, expectRowBecomesFinal } from '../helpers/subagentRegistry'
import { assistantBubbles, messageBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expectRowsInWorkflowGroup } from '../helpers/workflowGrouping'

clineTest.describe('Cline workflow grouping', () => {
  clineTest('groups two native teammate runs under their team', async ({ native }) => {
    const { page, modelScript } = native
    const teammates = [
      { agentId: 'reviewer', marker: 'CLINE_TEAM_REVIEWER' },
      { agentId: 'writer', marker: 'CLINE_TEAM_WRITER' },
    ] as const
    await modelScript.rule(...teammates.map(({ agentId, marker }) => ({
      name: `the ${agentId} answers`,
      when: { user: `Reply with ${marker}` },
      respond: { text: marker },
      once: true,
    })))
    // The queue holds two spawn steps, two run steps, and the final answer.
    const start = await modelScript.queue(
      ...teammates.map(({ agentId }) => ({ toolCalls: [clineSpawnTeammateToolCall(`spawn-${agentId}`, agentId, 'Answer the delegated task.')] })),
      ...teammates.map(({ agentId, marker }) => ({ toolCalls: [clineRunTeammateTaskToolCall(`run-${agentId}`, agentId, modelScript.prompt(`Reply with ${marker}.`))] })),
      { text: 'Both teammate runs were queued.' },
    )
    await modelScript.fallback({ text: 'The teammate runs finished.' })
    await sendMessage(page, modelScript.prompt('Spawn two teammates and run one task on each.'))
    const status = await modelScript.waitForSteps(start + 5)
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'Both teammate runs were queued.' }).first()).toBeVisible()

    const bodyAt = (offset: number) => stepRequest(status, start + offset).body
    expect(offeredTools(bodyAt(0))).toContain('team_spawn_teammate')
    expect(offeredTools(bodyAt(1))).toContain('team_spawn_teammate')
    expect(offeredTools(bodyAt(2))).toContain('team_run_task')
    expect(offeredTools(bodyAt(3))).toContain('team_run_task')
    // A request carries the result of the call that the step before it made, so the requests of the two steps after
    // the run steps carry the two run IDs.
    const runIDs: string[] = []
    for (const offset of [3, 4]) {
      const queuedBody = bodyAt(offset)
      if (!isObject(queuedBody) || !Array.isArray(queuedBody.messages))
        throw new Error('the Cline model request has no message list after an async run')
      const resultMessage = queuedBody.messages
        .filter(message => isObject(message) && message.role === 'tool' && typeof message.content === 'string' && message.content.includes('"runId"'))
        .at(-1)
      if (!isObject(resultMessage) || typeof resultMessage.content !== 'string')
        throw new Error('the Cline async run returned no tool result with a run ID')
      const result: unknown = JSON.parse(resultMessage.content)
      if (!isObject(result) || typeof result.runId !== 'string')
        throw new Error('the Cline async run result has no run ID')
      expect(result.mode).toBe('async')
      expect(result.runId).toMatch(/^run_/)
      runIDs.push(result.runId)
    }
    expect(new Set(runIDs).size).toBe(2)
    for (const { agentId } of teammates)
      await expect.poll(async () => (await modelScript.status()).ruleMatches[`the ${agentId} answers`] ?? 0).toBe(1)

    await expandBackgroundTasksSection(page)
    const runs = page.locator('[data-testid="bg-task-row"]:visible[data-kind="workflow"]')
    await expect(runs).toHaveCount(2)
    const teammateRuns = teammates.map(({ agentId }) => ({ agentId, run: runs.filter({ hasText: agentId }).first() }))
    for (const { agentId, run } of teammateRuns) {
      await expect(messageBubbles(page).filter({ hasText: `${agentId} started a run` }).first()).toBeVisible()
      await expectRowBecomesFinal(page, run)
      await expect(run).toHaveAttribute('data-status', 'completed')
    }
    // The team heading holds a suffix that the test cannot predict, so the check uses a pattern.
    await expectRowsInWorkflowGroup(teammateRuns.map(({ run }) => run), /^team-[\w-]{5}$/)
  })
})
