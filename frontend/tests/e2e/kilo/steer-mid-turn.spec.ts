import { exerciseSteerBeforeTool } from '../helpers/nativeToolSteering'
import { kiloTest } from '../kilo-fixtures'

kiloTest('places queued guidance in the next native model request', async ({ native }) => {
  await exerciseSteerBeforeTool(native, { approveTool: true, resultDividers: 2 })
})
