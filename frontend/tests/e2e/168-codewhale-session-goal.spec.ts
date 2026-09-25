/**
 * 168 -- Codewhale session goal, set and cleared through the goal card.
 *
 * A goal set starts a kickoff turn at once, and the runtime starts one more
 * pass after each turn until the model marks the goal complete or blocked. The
 * runtime has no pause or resume route, so the card offers Set and Clear only.
 *
 * The goal state is WORKER state that arrives on a broadcast, so every
 * assertion polls for the worker's answer rather than reading what the click
 * did.
 */
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { CODEWHALE_E2E_SKIP_REASON, codewhaleTest, expect } from './codewhale-fixtures'
import { blockGoalToolCall } from './helpers/providerToolCalls'
import { expandGoalsAndTodosSection, expectGoalStatus, goalAction, openGoalMenu } from './helpers/subagentRegistry'
import { sendMessage, waitForAgentIdle } from './helpers/ui'

codewhaleTest.skip(!!CODEWHALE_E2E_SKIP_REASON, CODEWHALE_E2E_SKIP_REASON || '')

codewhaleTest.describe('Codewhale session goal', () => {
  codewhaleTest('sets a goal that the model blocks, and clears it', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace

    // Drive one turn first, so the goal actions come from the running agent.
    await modelScript.queue({ text: 'ready' })
    await sendMessage(page, modelScript.prompt('Reply with the single word: ready'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expandGoalsAndTodosSection(page)
    await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()

    // The kickoff turn. The model marks the goal blocked through the runtime's
    // own goal tool, which ends the continuation passes, and then answers.
    await modelScript.queue(
      { toolCalls: [blockGoalToolCall(AgentProvider.CODEWHALE, 'goal-blocked', 'The scripted test stops here.')] },
      { text: 'The goal is blocked.' },
    )
    await goalAction(page, 'set').click()
    // The objective carries the marker, because the kickoff turn takes the
    // objective as its prompt.
    await page.locator('[data-testid="goal-editor"]:visible .ProseMirror').fill(modelScript.prompt('Keep the build green.'))
    await page.locator('[data-testid="set-goal-submit"]:visible').click()
    await expect(page.locator('[data-testid="goal-objective"]:visible')).toContainText('Keep the build green.')

    await modelScript.waitForSteps()
    await expectGoalStatus(page, 'blocked')
    await waitForAgentIdle(page)

    await openGoalMenu(page)
    await expect(goalAction(page, 'pause')).toHaveCount(0)
    await expect(goalAction(page, 'resume')).toHaveCount(0)
    await goalAction(page, 'clear').click()
    await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()

    // The clear reached the runtime, so the goal does not return after a reload.
    await page.reload()
    await expandGoalsAndTodosSection(page)
    await expect(page.locator('[data-testid="goal-card-empty"]:visible')).toBeVisible()
  })
})
