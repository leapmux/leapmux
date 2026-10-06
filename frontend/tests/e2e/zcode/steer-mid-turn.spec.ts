import { exerciseQueuedTurnWithoutSteering } from '../helpers/nativeToolSteering'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('offers no Steer control while the native prompt holds the session', async ({ native }) => {
  await exerciseQueuedTurnWithoutSteering(native)
})
