import { exerciseSteerBeforeTool, exerciseSteerDuringText } from '../helpers/nativeToolSteering'
import { kiloTest } from '../kilo-fixtures'

kiloTest('places queued guidance in the next native model request', async ({ native }) => {
  await exerciseSteerBeforeTool(native, { approveTool: true, resultDividers: 2 })
})

kiloTest('places a steer after the complete answer that precedes it', async ({ native }) => {
  await exerciseSteerDuringText(native, { answer: 'continue', splitAnswer: true })
})
