import { exerciseQueuedTurnWithoutSteering } from '../helpers/nativeToolSteering'
import { lettaTest } from '../letta-fixtures'

lettaTest('refuses mid-turn steering and runs the queued prompt in a separate native turn', async ({ native }) => {
  await exerciseQueuedTurnWithoutSteering(native)
})
