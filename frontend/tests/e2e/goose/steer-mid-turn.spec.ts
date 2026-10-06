import { gooseTest } from '../goose-fixtures'
import { exerciseQueuedTurnWithoutSteering } from '../helpers/nativeToolSteering'

gooseTest('offers no Steer control when the native handshake omits it', async ({ native }) => {
  await exerciseQueuedTurnWithoutSteering(native)
})
