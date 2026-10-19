import { clineTest } from '../cline-fixtures'
import { exerciseSteerAfterTool, exerciseSteerDuringText } from '../helpers/nativeToolSteering'

clineTest.describe('Cline steering', () => {
  clineTest('steers a running turn after its tool', async ({ native }) => {
    await exerciseSteerAfterTool(native)
  })

  clineTest('keeps the cut answer interrupted when a steer stops the stream', async ({ native }) => {
    await exerciseSteerDuringText(native, { answer: 'preempt', steerOrder: 'any' })
  })
})
