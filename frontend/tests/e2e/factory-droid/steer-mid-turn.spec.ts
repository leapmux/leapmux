import { droidTest } from '../droid-fixtures'
import { exerciseSteerBeforeTool } from '../helpers/nativeToolSteering'

droidTest.describe('Factory Droid mid-turn steering', () => {
  droidTest('places queued guidance in the next native model request', async ({ native }) => {
    await exerciseSteerBeforeTool(native)
  })
})
