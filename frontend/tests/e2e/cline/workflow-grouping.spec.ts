import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { clineTest, offeredTools } from '../cline-fixtures'
import { clineRunTeammateTaskToolCall, clineSpawnTeammateToolCall } from '../helpers/providerToolCalls'
import { expandBackgroundTasksSection, expectRowBecomesFinal } from '../helpers/subagentRegistry'
import { assistantBubbles, messageBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expectRowsInWorkflowGroup } from '../helpers/workflowGrouping'

clineTest.describe('Cline workflow grouping', () => {
  clineTest('groups two native teammate runs under their team', async ({ authenticatedClineWorkspace, page, modelScript }) => {
    void authenticatedClineWorkspace
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
    await modelScript.queue(
      ...teammates.map(({ agentId }) => ({ toolCalls: [clineSpawnTeammateToolCall(`spawn-${agentId}`, agentId, 'Answer the delegated task.')] })),
      ...teammates.map(({ agentId, marker }) => ({ toolCalls: [clineRunTeammateTaskToolCall(`run-${agentId}`, agentId, modelScript.prompt(`Reply with ${marker}.`))] })),
      { text: 'Both teammate runs were queued.' },
    )
    await modelScript.fallback({ text: 'The teammate runs finished.' })
    await sendMessage(page, modelScript.prompt('Spawn two teammates and run one task on each.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'Both teammate runs were queued.' }).first()).toBeVisible()

    const status = await modelScript.status()
    expect(offeredTools(status.requests.find(request => request.stepIndex === 0)?.body)).toContain('team_spawn_teammate')
    expect(offeredTools(status.requests.find(request => request.stepIndex === 1)?.body)).toContain('team_spawn_teammate')
    expect(offeredTools(status.requests.find(request => request.stepIndex === 2)?.body)).toContain('team_run_task')
    expect(offeredTools(status.requests.find(request => request.stepIndex === 3)?.body)).toContain('team_run_task')
    const runIDs: string[] = []
    for (const stepIndex of [3, 4]) {
      const queuedBody = status.requests.find(request => request.stepIndex === stepIndex)?.body
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
