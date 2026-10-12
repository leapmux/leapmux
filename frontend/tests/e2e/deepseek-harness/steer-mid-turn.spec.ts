import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseSteerAfterTool } from '../helpers/nativeToolSteering'

deepseekHarnessTest('delivers steering to the current native turn before its real command ends', async ({ native }) => {
  await exerciseSteerAfterTool(native, { expectDisplayedOutput: false })
})
