import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codewhaleTest } from '../codewhale-fixtures'
import { expandGoalsAndTodosSection, expectEmptyGoalCard, expectGoalObjective, expectGoalStatus, goalAction, openGoalMenu, scriptedObjective, submitGoal } from '../helpers/goalsAndTodos'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { blockGoalToolCall } from '../helpers/providerToolCalls'
import { waitForAgentIdle } from '../helpers/ui'

/**
 * The goal card sets and clears the actual native session goal. The test waits for authoritative Worker state.
 *
 * The Worker drives Codewhale's runtime API. Codewhale stores each thread and its tool results in its private native store.
 *
 * Setting a goal starts a turn at once. Codewhale starts another pass after each turn until the model completes or blocks the goal. The runtime exposes no pause or resume route.
 */
codewhaleTest.describe('Codewhale session goal', () => {
  codewhaleTest('sets a goal that the model blocks, and clears it', async ({ native }) => {
    const { page, modelScript } = native

    // Drive one turn first, so the goal actions come from the running agent.
    await sendNativeAnswer(native, 'Reply with the single word: ready', 'ready')

    await expandGoalsAndTodosSection(page)
    await expectEmptyGoalCard(page)

    // The kickoff turn. The model marks the goal blocked through the runtime's
    // own goal tool, which ends the continuation passes, and then answers.
    await modelScript.queue(
      { toolCalls: [blockGoalToolCall(AgentProvider.CODEWHALE, 'goal-blocked', 'The scripted test stops here.')] },
      { text: 'The goal is blocked.' },
    )
    // The objective carries the marker, because the kickoff turn takes the
    // objective as its prompt.
    const objective = scriptedObjective(modelScript, 'Keep the build green.')
    await submitGoal(page, objective.input)
    await expectGoalObjective(page, objective)

    await modelScript.waitForSteps()
    await expectGoalStatus(page, 'blocked')
    await waitForAgentIdle(page)

    await openGoalMenu(page)
    await expect(goalAction(page, 'pause')).toHaveCount(0)
    await expect(goalAction(page, 'resume')).toHaveCount(0)
    await goalAction(page, 'clear').click()
    await expectEmptyGoalCard(page)

    // The clear reached the runtime, so the goal does not return after a reload.
    await page.reload()
    await expandGoalsAndTodosSection(page)
    await expectEmptyGoalCard(page)
  })
})
