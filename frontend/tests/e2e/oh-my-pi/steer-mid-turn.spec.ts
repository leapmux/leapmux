import { exerciseSteerBeforeTool } from '../helpers/nativeToolSteering'
import { ohMyPiTest } from '../ohmypi-fixtures'

/**
 * The queued input enters the same native turn after the running tool. The next native request must carry the input.
 *
 * The Worker drives `omp --mode rpc-ui` through its JSON Lines protocol.
 */
ohMyPiTest('steers a queued message into the active turn', async ({ native }) => {
  await exerciseSteerBeforeTool(native)
})
