import { exerciseSteerBeforeTool } from '../helpers/nativeToolSteering'
import { museTest } from '../muse-fixtures'

museTest('delivers new input before the active native turn ends', async ({ native }) => {
  await exerciseSteerBeforeTool(native)
})
