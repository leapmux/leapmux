import { expect } from '@playwright/test'
import { withCleanup } from '../helpers/cleanup'
import { clearGoal, expectGoalObjective, expectGoalStatus, scriptedObjective, submitGoal } from '../helpers/goalsAndTodos'
import { waitForAgentIdle } from '../helpers/ui'
import { reasonixTest } from '../reasonix-fixtures'
import { exerciseReasonixGoalErrorResume, exerciseReasonixGoalPauseResume } from './goalScenario'

reasonixTest('pauses and resumes the retained native goal through its advertised requests after reload', async ({ native }) => {
  await exerciseReasonixGoalPauseResume(native)
})

reasonixTest('resumes the retained native goal through the Worker API after a model error', async ({ native }) => {
  await exerciseReasonixGoalErrorResume(native)
})

reasonixTest('accepts a goal whose native status redacts its objective', async ({ native }) => {
  const { page, modelScript } = native
  const objective = scriptedObjective(modelScript, 'Remove the leaked key sk-real-secret-value-123456')
  const gate = `reasonix-redacted-goal-${crypto.randomUUID()}`
  const start = await modelScript.queue({ text: 'Hold the accepted goal.', gate })
  await withCleanup(async () => {
    await submitGoal(page, objective.input)
    await modelScript.waitForGate(gate)
    await expect(page.getByTestId('set-goal-dialog')).toHaveCount(0)
    await expectGoalObjective(page, { ...objective, text: objective.text.replace('sk-real-secret-value-123456', 'sk-rea*****************3456') })
    await expectGoalStatus(page, 'active')
    await clearGoal(page)
    await modelScript.releaseGateIfHeld(gate)
    await waitForAgentIdle(page)
    expect((await modelScript.waitForSteps(start + 1)).unexpectedRequests).toEqual([])
  }, async () => { await modelScript.releaseGateIfHeld(gate) })
})
