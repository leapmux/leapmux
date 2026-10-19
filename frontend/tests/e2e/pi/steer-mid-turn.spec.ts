import { exerciseSteerBeforeTool, exerciseSteerDuringText } from '../helpers/nativeToolSteering'
import { piTest } from '../pi-fixtures'

piTest('places queued guidance in the next native model request', async ({ native }) => {
  await exerciseSteerBeforeTool(native)
})

piTest('places a steer after the complete answer that precedes it', async ({ native }) => {
  await exerciseSteerDuringText(native, { answer: 'continue' })
})
