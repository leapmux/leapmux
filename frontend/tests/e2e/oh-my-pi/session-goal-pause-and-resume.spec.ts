import { clearGoal, expandGoalsAndTodosSection, expectEmptyGoalCard, expectGoalObjective, expectGoalStatus, goalAction, openGoalMenu, scriptedObjective, setGoal } from '../helpers/goalsAndTodos'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { waitForAgentIdle } from '../helpers/ui'
import { ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest('pauses native goal continuation and resumes it after a page reload', async ({ native }) => {
  const { page, modelScript } = native
  await sendNativeAnswer(native, 'Prepare this goal session.', 'Ready for a goal.')
  const firstGate = 'omp-goal-first-round'
  const resumedGate = 'omp-goal-resumed-round'
  const objective = scriptedObjective(modelScript, 'Keep this objective until I clear it.')
  await modelScript.fallback({ text: 'The first goal round ended.', gate: firstGate })
  try {
    await setGoal(page, objective, async () => {
      await modelScript.waitForGate(firstGate)
    })
    await openGoalMenu(page)
    await goalAction(page, 'pause').click()
    await expectGoalStatus(page, 'paused')
    // Oh My Pi pauses continuation immediately but lets the current round finish.
    await modelScript.releaseGate(firstGate)
    await waitForAgentIdle(page)
    await page.reload()
    await expandGoalsAndTodosSection(page)
    await expectGoalObjective(page, objective)
    await expectGoalStatus(page, 'paused')

    await modelScript.fallback({ text: 'The resumed goal round ended.', gate: resumedGate })
    await openGoalMenu(page)
    await goalAction(page, 'resume').click()
    await modelScript.waitForGate(resumedGate)
    await expectGoalStatus(page, 'active')
    await clearGoal(page)
    await expectEmptyGoalCard(page)
    await modelScript.releaseGate(resumedGate)
    await waitForAgentIdle(page)
  }
  finally {
    await modelScript.releaseGateIfHeld(firstGate)
    await modelScript.releaseGateIfHeld(resumedGate)
  }
})
