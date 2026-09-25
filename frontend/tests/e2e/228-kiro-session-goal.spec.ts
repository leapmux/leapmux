import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { MAX_STEP_DELAY_MS } from './helpers/mockModelScript'
import { blockGoalToolCall, completeGoalToolCall } from './helpers/providerToolCalls'
import { expandGoalsAndTodosSection, expectGoalStatus, expectRegistryRow, goalAction, openGoalMenu } from './helpers/subagentRegistry'
import { messageBubbles, openWorkspace, waitForSettingsHydrated } from './helpers/ui'
import { expect, KIRO_E2E_SKIP_REASON, kiroTest, openKiroAgent } from './kiro-fixtures'

kiroTest.skip(!!KIRO_E2E_SKIP_REASON, KIRO_E2E_SKIP_REASON || '')

const KIRO = AgentProvider.KIRO

/**
 * The words each step of Kiro's goal workflow opens its prompt with. The step quotes
 * the user's `/goal` command there, and that command carries the marker.
 */
const GOAL_STEP_PROMPT = 'original_user_request'

/**
 * How long each step of the pause test holds its model call open: the longest hold
 * that the mock takes. The pause, the resume and the clear each end the held call,
 * so no test waits this long, and Kiro's own round limit cannot pause the goal
 * first.
 */
const GOAL_STEP_HOLD_MS = MAX_STEP_DELAY_MS

/** The word of the reason that Kiro states when a goal reaches its round limit. */
const KIRO_ROUND_LIMIT_WORD = 'maxIterations'

/**
 * 228 -- Kiro session goal.
 *
 * The goal card drives Kiro's `/goal` command, and Kiro runs the goal as a workflow
 * of its own: a loop of steps, each in a session of its own. The workflow's
 * notifications state the goal, and the workflow's own requests pause, resume and
 * cancel it. The Allow all policy keeps each step free of permission requests.
 */
kiroTest.describe('Kiro session goal', () => {
  kiroTest('sets a goal that a step completes, with the workflow in the registry', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openKiroAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { policyPreset: 'allow-all' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)
    // The first step reports success. Its session then ends its turn with words.
    // Kiro wakes the parent session once the workflow ends, and the number of
    // such turns is Kiro's own, so the fallback answers them.
    await modelScript.rule({
      name: 'the first step reports success',
      when: { protocol: 'aws-event-stream', user: GOAL_STEP_PROMPT },
      respond: { toolCalls: [completeGoalToolCall(KIRO, 'kiro-goal-done')] },
      once: true,
    })
    await modelScript.fallback({ text: 'Recorded.' })

    await expandGoalsAndTodosSection(page)
    await goalAction(page, 'set').click()
    await page.locator('[data-testid="goal-editor"]:visible .ProseMirror').fill(modelScript.prompt('Write the release notes.'))
    await page.locator('[data-testid="set-goal-submit"]:visible').click()
    await expect(page.locator('[data-testid="goal-objective"]:visible')).toContainText('Write the release notes.')

    await expectGoalStatus(page, 'done')
    // The workflow run and its step each take a registry row.
    await expectRegistryRow(page, { titleContains: 'goal · work #1' })
  })

  // A step that reports an error fails the run. The goal card shows the words of
  // the step, not Kiro's generic reason for the failed node, and the words also
  // reach the parent transcript.
  kiroTest('leaves the goal blocked when a step reports an error', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openKiroAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { policyPreset: 'allow-all' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)
    await modelScript.rule({
      name: 'the first step reports an error',
      when: { protocol: 'aws-event-stream', user: GOAL_STEP_PROMPT },
      respond: { toolCalls: [blockGoalToolCall(KIRO, 'kiro-goal-blocked', 'The repository is read-only.')] },
      once: true,
    })
    // The step then ends its turn with words, and Kiro wakes the parent session for
    // the step's message and for the failed run.
    await modelScript.fallback({ text: 'Recorded.' })

    await expandGoalsAndTodosSection(page)
    await goalAction(page, 'set').click()
    await page.locator('[data-testid="goal-editor"]:visible .ProseMirror').fill(modelScript.prompt('Rewrite the history.'))
    await page.locator('[data-testid="set-goal-submit"]:visible').click()
    await expect(page.locator('[data-testid="goal-objective"]:visible')).toContainText('Rewrite the history.')

    await expectGoalStatus(page, 'blocked')
    await expect(page.locator('[data-testid="goal-status-detail"]:visible')).toContainText('The repository is read-only.')
    await expect(messageBubbles(page).filter({ hasText: 'The repository is read-only.' }).first()).toBeVisible()
  })

  // Kiro pauses a goal by itself after five rounds, and that pause would also show
  // the paused status. So each step holds its model call open for longer than the
  // test takes, which keeps the run in its first round, and the test checks that
  // the pause is not the round limit. The pause cuts the held call short, and the
  // resume starts the step again, which a new model call proves.
  kiroTest('pauses, resumes and clears a running goal', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openKiroAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { policyPreset: 'allow-all' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)
    await modelScript.fallback({ text: 'Still working on the objective.', delayMs: GOAL_STEP_HOLD_MS })

    await expandGoalsAndTodosSection(page)
    await goalAction(page, 'set').click()
    await page.locator('[data-testid="goal-editor"]:visible .ProseMirror').fill(modelScript.prompt('Keep inspecting the repository.'))
    await page.locator('[data-testid="set-goal-submit"]:visible').click()
    await expect(page.locator('[data-testid="goal-objective"]:visible')).toContainText('Keep inspecting the repository.')
    await expectGoalStatus(page, 'active')
    // The first step holds its model call open, so the run is inside its first round.
    await expect.poll(async () => (await modelScript.status()).requests.length).toBeGreaterThan(0)

    await openGoalMenu(page)
    await goalAction(page, 'pause').click()
    await expectGoalStatus(page, 'paused')
    const detail = page.locator('[data-testid="goal-status-detail"]:visible')
    await expect(detail.filter({ hasText: KIRO_ROUND_LIMIT_WORD }), 'the reader paused the goal, not the round limit').toHaveCount(0)
    const callsWhilePaused = (await modelScript.status()).requests.length

    await openGoalMenu(page)
    await goalAction(page, 'resume').click()
    await expectGoalStatus(page, 'active')
    await expect.poll(async () => (await modelScript.status()).requests.length, { message: 'the resumed run calls the model again' }).toBeGreaterThan(callsWhilePaused)

    await openGoalMenu(page)
    await goalAction(page, 'clear').click()
    await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
  })
})
