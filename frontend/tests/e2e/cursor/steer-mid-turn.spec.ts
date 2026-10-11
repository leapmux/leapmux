import { cursorTest } from '../cursor-fixtures'
import { exerciseQueuedTurnWithoutSteering } from '../helpers/nativeToolSteering'

cursorTest('queues a normal prompt until the native turn ends without a steering route', async ({ native }) => {
  await exerciseQueuedTurnWithoutSteering(native)
})
