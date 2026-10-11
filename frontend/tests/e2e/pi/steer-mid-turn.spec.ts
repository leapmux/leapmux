import { exerciseSteerBeforeTool } from '../helpers/nativeToolSteering'
import { piTest } from '../pi-fixtures'

piTest('places queued guidance in the next native model request', async ({ native }) => {
  await exerciseSteerBeforeTool(native)
})
