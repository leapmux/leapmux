import { commandCodeTest } from '../command-code-fixtures'
import { exerciseSteerAfterTool } from '../helpers/nativeToolSteering'

commandCodeTest('delivers new text to the actual running native turn', async ({ native }) => {
  await exerciseSteerAfterTool(native, { expectDisplayedOutput: false })
})
