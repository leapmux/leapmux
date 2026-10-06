import { clineTest } from '../cline-fixtures'
import { exerciseSteerAfterTool } from '../helpers/nativeToolSteering'

clineTest.describe('Cline steering', () => {
  clineTest('steers a running turn after its tool', async ({ native }) => {
    await exerciseSteerAfterTool(native)
  })
})
