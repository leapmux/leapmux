import { grokTest } from '../grok-fixtures'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'

grokTest('reopens the native picker handle and restores the saved transcript', async ({ native }) => {
  await exerciseSessionResume(native)
})
