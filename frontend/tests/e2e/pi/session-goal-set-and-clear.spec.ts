import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { withCleanup } from '../helpers/cleanup'
import { pauseResumeClearGoal, scriptedObjective, setGoal } from '../helpers/goalsAndTodos'
import { piTest } from '../pi-fixtures'
import { approvePiGoalClear, exercisePiGoalPanel } from './goalScenario'

piTest('pauses and resumes a native goal after reload', async ({ authenticatedPiWorkspace, page, modelScript }) => {
  void authenticatedPiWorkspace
  const gate = 'pi-goal-lifecycle'
  await modelScript.queue({ text: 'The first goal turn finished.' })
  await modelScript.fallback({ text: 'The next goal turn is held.', gate })
  const objective = scriptedObjective(modelScript, 'Keep the Pi session goal until the browser clears it.')
  await withCleanup(async () => {
    await setGoal(page, objective, async () => {
      await modelScript.waitForSteps(1)
      await modelScript.waitForGate(gate)
    })
    await pauseResumeClearGoal(page, objective, { afterClear: () => approvePiGoalClear(page) })
  }, async () => {
    await modelScript.releaseGateIfHeld(gate)
  })
})

piTest('controls a real Pi goal through the shared goal panel and confirmation', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.PI }
  await exercisePiGoalPanel(context)
})
