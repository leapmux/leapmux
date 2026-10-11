import { ampTest } from '../amp-fixtures'
import { exerciseSteerAfterTool } from '../helpers/nativeToolSteering'

// Amp's stream JSON states a shell command only in its call and in its result, so the
// running row shows no output. The steering line reaches the model after the tool result.
ampTest('steers a running turn after its tool', async ({ native }) => {
  await exerciseSteerAfterTool(native, { expectDisplayedOutput: false })
})
