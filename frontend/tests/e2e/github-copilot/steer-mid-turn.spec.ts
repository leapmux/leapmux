import { copilotTest } from '../copilot-fixtures'
import { exerciseSteerBeforeTool, exerciseSteerDuringText, exerciseStopWithPendingSteer } from '../helpers/nativeToolSteering'
import { COPILOT_STEER_COMPLETION_RULE } from './steeringScenario'

copilotTest('places queued guidance in the next native model request', async ({ native }) => {
  await native.modelScript.rule(COPILOT_STEER_COMPLETION_RULE)
  await exerciseSteerBeforeTool(native, { approveTool: true })
})

copilotTest('places a steer after the complete answer that precedes it', async ({ native }) => {
  await exerciseSteerDuringText(native, { answer: 'continue' })
})

copilotTest('returns an unread steer to the queue after a stop', async ({ native }) => {
  await exerciseStopWithPendingSteer(native)
})
