import { codewhaleTest } from '../codewhale-fixtures'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'

codewhaleTest('reopens the native picker handle and restores the saved transcript', async ({ native }) => {
  await exerciseSessionResume(native)
})
