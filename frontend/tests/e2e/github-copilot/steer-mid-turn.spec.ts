import { copilotTest } from '../copilot-fixtures'
import { exerciseSteerBeforeTool } from '../helpers/nativeToolSteering'

copilotTest('places queued guidance in the next native model request', async ({ native }) => {
  await exerciseSteerBeforeTool(native, { approveTool: true })
})
