import { gooseTest } from '../goose-fixtures'
import { exerciseQueuedTurnWithoutSteering } from '../helpers/nativeToolSteering'

gooseTest('offers no Steer control when the native handshake omits it', async ({ native }) => {
  await exerciseQueuedTurnWithoutSteering(native)
})

// Track this LeapMux limitation: https://github.com/leapmux/leapmux/issues/519.
