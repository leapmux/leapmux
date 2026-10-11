import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { mimoTest } from '../mimo-fixtures'

mimoTest('reopens the native picker handle and restores the saved transcript', async ({ native }) => {
  await exerciseSessionResume(native)
})
