import { AgentGoalAction, ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { getTestChannel } from './helpers/api'
import { junieAnswerToolCall } from './helpers/providerToolCalls'
import { queuedInputRow, steerButton } from './helpers/steer'
import { goalAction } from './helpers/subagentRegistry'
import { assistantBubbles, sendMessage, waitForAgentIdle, waitForSettingsHydrated } from './helpers/ui'
import { expect, JUNIE_E2E_SKIP_REASON, junieTest } from './junie-fixtures'

junieTest.skip(!!JUNIE_E2E_SKIP_REASON, JUNIE_E2E_SKIP_REASON || '')

junieTest.describe('junie unsupported controls', () => {
  junieTest('does not offer Set for the session goal', async ({ authenticatedJunieWorkspace, page, leapmuxServer }) => {
    void authenticatedJunieWorkspace
    await waitForSettingsHydrated(page)
    const agentId = await page.locator('[data-testid="tab"][data-tab-type="agent"]:visible').first().getAttribute('data-tab-id')
    expect(agentId).not.toBeNull()
    const channel = await getTestChannel(leapmuxServer.hubUrl, leapmuxServer.adminToken)
    let actions: AgentGoalAction[] = []
    await expect.poll(async () => {
      const response = await channel.callWorker(
        leapmuxServer.workerId,
        'ListAgentMessages',
        ListAgentMessagesRequestSchema,
        ListAgentMessagesResponseSchema,
        { agentId: agentId ?? '', limit: 1 },
      )
      actions = response.goalSupportedActions
      return actions
    }).toContain(AgentGoalAction.CLEAR)
    expect(actions).not.toContain(AgentGoalAction.SET)
    await expect(goalAction(page, 'set')).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Set a goal' })).toHaveCount(0)
  })

  junieTest('runs a steered prompt in the next turn', async ({ authenticatedJunieWorkspace, page, modelScript }) => {
    void authenticatedJunieWorkspace
    const firstPrompt = modelScript.prompt('Answer the first request.')
    const secondPrompt = modelScript.prompt('Answer the second request after the first.')
    const firstGate = 'junie-first-turn'
    await modelScript.rule(
      { name: 'junie-capability-filter', when: { system: 'capability filter agent' }, respond: { text: '' } },
      { name: 'junie-task-name', when: { system: 'task description summarizer' }, respond: { text: 'Queued request' } },
    )
    await modelScript.queue(
      { gate: firstGate, toolCalls: [junieAnswerToolCall('junie-first-answer', 'FIRST_JUNIE_ANSWER')] },
      { toolCalls: [junieAnswerToolCall('junie-second-answer', 'SECOND_JUNIE_ANSWER')] },
    )

    await sendMessage(page, firstPrompt)
    await modelScript.waitForGate(firstGate)
    try {
      await sendMessage(page, secondPrompt)
      const queued = queuedInputRow(page, 'Answer the second request')
      await expect(queued).toBeVisible()
      await expect(steerButton(queued)).toBeVisible()
      await steerButton(queued).click()
      expect((await modelScript.status()).requests.some(request => request.stepIndex === 1)).toBe(false)
    }
    finally {
      if ((await modelScript.status()).pendingGates.includes(firstGate))
        await modelScript.releaseGate(firstGate)
    }

    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
    const first = status.requests.find(request => request.stepIndex === 0)
    const second = status.requests.find(request => request.stepIndex === 1)
    expect(JSON.stringify(first?.body ?? {}).includes('Answer the second request')).toBe(false)
    expect(JSON.stringify(second?.body ?? {}).includes('Answer the second request')).toBe(true)
    await expect(assistantBubbles(page).filter({ hasText: 'FIRST_JUNIE_ANSWER' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'SECOND_JUNIE_ANSWER' }).first()).toBeVisible()
    await expect(page.locator('[data-testid="result-divider"]:visible')).toHaveCount(2)
  })
})
