import { gooseTest } from '../goose-fixtures'
import { exerciseTextGoalQueue, expectGoalStatus, scriptedObjective, setGoal } from '../helpers/goalsAndTodos'
import { waitForAgentIdle } from '../helpers/ui'

gooseTest('queues and observes session-goal commands', async ({ native }) => {
  await exerciseTextGoalQueue(native, {
    objective: 'Wait for the Goose goal route unlock.',
    clearCommand: '/goal off',
  })
})

// Goose states no structured goal report: the worker infers the goal's end from
// the texts of the round that the "Goal: " notice arms. A clean final message
// completes the goal, and a round that never reached the model keeps it. One
// goal round asks the model more than once, so the fallback answers every ask.
gooseTest('marks the goal achieved when its round ends with a clean final message', async ({ native }) => {
  const { page, modelScript } = native
  const objective = scriptedObjective(modelScript, 'Finish the probe so Goose reports the goal achieved.')
  await modelScript.fallback({ text: 'GOAL_ACHIEVED_ANSWER The round finished cleanly.' })
  await setGoal(page, objective)
  await waitForAgentIdle(page)
  await expectGoalStatus(page, 'done')
})

gooseTest('keeps the goal active when the round fails to reach the model', async ({ native }) => {
  const { page, modelScript } = native
  const objective = scriptedObjective(modelScript, 'Stay active while the service fails the round.')
  await modelScript.fallback({ error: { status: 503, message: 'probe stub: service unavailable' } })
  await setGoal(page, objective)
  await waitForAgentIdle(page)
  await expectGoalStatus(page, 'active')
})
