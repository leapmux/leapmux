import { fastAgentTest } from '../fastagent-fixtures'
import { exerciseQueuedTurnWithoutSteering } from '../helpers/nativeToolSteering'

fastAgentTest('refuses mid-turn steering and runs the queued prompt in a separate native turn', async ({ native }) => {
  await exerciseQueuedTurnWithoutSteering(native)
})
