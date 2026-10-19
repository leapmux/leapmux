import { droidTest } from '../droid-fixtures'
import { exerciseSteerBeforeTool, exerciseStopWithPendingSteer } from '../helpers/nativeToolSteering'

droidTest.describe('Factory Droid mid-turn steering', () => {
  droidTest('places queued guidance in the next native model request', async ({ native }) => {
    await exerciseSteerBeforeTool(native)
  })

  droidTest('returns an unread steer to the queue after a stop and sends it once', async ({ native }) => {
    await exerciseStopWithPendingSteer(native)
  })
})
