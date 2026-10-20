import { exerciseSteerBeforeTool, exerciseSteerDuringText } from '../helpers/nativeToolSteering'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('places queued guidance in the next native model request', async ({ native }) => {
  await exerciseSteerBeforeTool(native)
})

opencodeTest('places a steer after the complete answer that precedes it', async ({ native }) => {
  await exerciseSteerDuringText(native, { answer: 'continue', splitAnswer: true })
})
