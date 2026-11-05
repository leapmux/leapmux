import { antigravityTest } from '../antigravity-fixtures'
import { exerciseSteerDuringText } from '../helpers/nativeToolSteering'

antigravityTest('delivers a steer into the running native turn and preserves its order', async ({ native }) => {
  await exerciseSteerDuringText(native, {
    answer: 'continue',
    steerOrder: 'any',
    progress: 'model-stream',
  })
})
