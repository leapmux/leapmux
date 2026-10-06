import { exerciseSteerBeforeTool } from '../helpers/nativeToolSteering'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('places queued guidance in the next native model request', async ({ native }) => {
  await exerciseSteerBeforeTool(native)
})
