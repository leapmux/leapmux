import { exerciseSteerBeforeTool } from '../helpers/nativeToolSteering'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('places queued guidance in the next native model request', async ({ native }) => {
  await exerciseSteerBeforeTool(native)
})
