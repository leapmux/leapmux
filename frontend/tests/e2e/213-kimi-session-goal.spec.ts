/**
 * 213 — Kimi Code session goal.
 *
 * The kap-server owns the goal and reports each change with `goal.updated`.
 * LeapMux writes it through the session profile, and creating a goal starts no
 * turn, so the worker sends the objective as the user's message. Every
 * assertion polls for the worker's answer, because the goal is worker state
 * that arrives on a broadcast.
 */
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { completeGoalToolCall, createGoalToolCall } from './helpers/providerToolCalls'
import { expandGoalsAndTodosSection, expectGoalStatus, goalAction, goalCard, openGoalMenu } from './helpers/subagentRegistry'
import { assistantBubbles, expectSettingsChip, sendMessage, waitForAgentIdle, waitForControlBanner, waitForSettingsHydrated } from './helpers/ui'
import { expect, KIMI_E2E_SKIP_REASON, kimiTest } from './kimi-fixtures'

kimiTest.skip(!!KIMI_E2E_SKIP_REASON, KIMI_E2E_SKIP_REASON || '')

const KIMI = AgentProvider.KIMI_CODE

kimiTest.describe('Kimi Code session goal', () => {
  kimiTest('set a goal from the panel, pause it, resume it, and clear it', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
    void authenticatedKimiWorkspace
    await waitForSettingsHydrated(page)

    // An active goal makes Kimi Code start one continuation turn after
    // another, and how many run between two clicks belongs to the provider. A
    // fallback answers them all. The DELAY keeps that loop at the pace of a
    // live model rather than at mock speed.
    await modelScript.fallback({ text: 'Working on the objective.', delayMs: 1000 })

    await expandGoalsAndTodosSection(page)
    await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
    await goalAction(page, 'set').click()
    // The objective carries the marker, because the worker sends it as the
    // message that starts the goal's first turn.
    await page.locator('[data-testid="goal-editor"]:visible .ProseMirror').fill(
      modelScript.prompt('Keep inspecting this repository until I pause the goal.'),
    )
    await page.locator('[data-testid="set-goal-submit"]:visible').click()

    await expect(goalCard(page)).toBeVisible()
    await expect.poll(async () =>
      await page.locator('[data-testid="goal-objective"]:visible').textContent(),
    ).toContain('Keep inspecting this repository')
    await expectGoalStatus(page, 'active')
    await expect.poll(async () => (await modelScript.status()).requests.length).toBeGreaterThan(0)

    await openGoalMenu(page)
    await goalAction(page, 'pause').click()
    await expectGoalStatus(page, 'paused')

    await page.reload()
    await expandGoalsAndTodosSection(page)
    await expectGoalStatus(page, 'paused')

    await openGoalMenu(page)
    await goalAction(page, 'resume').click()
    await expectGoalStatus(page, 'active')

    await openGoalMenu(page)
    await goalAction(page, 'clear').click()
    await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
  })

  // The model can start a goal too. Outside Never Ask the server asks first,
  // and each answer can also switch the permission mode the goal runs in.
  kimiTest('a goal the model starts asks first, and the model can complete it', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
    void authenticatedKimiWorkspace
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Always Ask')

    await modelScript.queue(
      { toolCalls: [createGoalToolCall(KIMI, 'create-goal', 'Reply with KIMI_GOAL_DONE once.')] },
      { toolCalls: [completeGoalToolCall(KIMI, 'complete-goal')] },
      { text: 'KIMI_GOAL_DONE' },
    )
    await sendMessage(page, modelScript.prompt('Start a goal to reply with KIMI_GOAL_DONE once, then complete it.'))
    await modelScript.waitForSteps(1)

    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('CreateGoal')
    await page.getByTestId('control-more-actions').click()
    await page.getByTestId('control-decision-goal_mode:yolo').click()
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()
    // The answer switched the mode the goal runs in.
    await expectSettingsChip(page, 'Ask When Needed')

    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'KIMI_GOAL_DONE' })).not.toHaveCount(0)
    // The server removes a completed goal, and the card follows it.
    await expandGoalsAndTodosSection(page)
    await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
  })
})
