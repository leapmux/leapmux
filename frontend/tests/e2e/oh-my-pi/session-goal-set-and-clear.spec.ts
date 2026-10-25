import { clearGoal, expectEmptyGoalCard, scriptedObjective, setGoal } from '../helpers/goalsAndTodos'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { waitForAgentIdle } from '../helpers/ui'
import { ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest('starts native goal work from the sidebar and clears the objective', async ({ native }) => {
  const { page, modelScript } = native
  await sendNativeAnswer(native, 'Prepare this goal session.', 'Ready for a goal.')
  // Each create starts its own native turn. Clearing must also permit another goal.
  for (const [index, text] of ['Keep the first objective until I clear it.', 'Keep the second objective until I clear it.'].entries()) {
    // A released model hold stays released, so each goal needs its own hold.
    const gate = `omp-goal-create-${index}`
    await modelScript.fallback({ text: 'The held goal round ended.', gate })
    try {
      await setGoal(page, scriptedObjective(modelScript, text), async () => {
        await modelScript.waitForGate(gate)
      })
      await clearGoal(page)
      await expectEmptyGoalCard(page)
      await modelScript.releaseGate(gate)
      await waitForAgentIdle(page)
    }
    finally {
      await modelScript.releaseGateIfHeld(gate)
    }
  }
})
