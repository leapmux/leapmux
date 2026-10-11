import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest('reopens the native picker handle and restores the saved transcript', async ({ native }) => {
  await exerciseSessionResume(native)
})
