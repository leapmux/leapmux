import { commandCodeTest } from '../command-code-fixtures'
import { exerciseSteerAfterTool, exerciseSteerDuringText, exerciseStopWithPendingSteer } from '../helpers/nativeToolSteering'

commandCodeTest('delivers new text to the actual running native turn', async ({ native }) => {
  await exerciseSteerAfterTool(native, { expectDisplayedOutput: false })
})

commandCodeTest('places a steer after the complete answer that precedes it', async ({ native }) => {
  await exerciseSteerDuringText(native, { answer: 'continue' })
})

commandCodeTest('returns an unread steer to the queue after a stop', async ({ native }) => {
  await exerciseStopWithPendingSteer(native)
})
