import { exerciseSessionReset } from '../helpers/nativeLifecycle'
import { ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest('clears the native context while the saved transcript stays visible', async ({ native }) => {
  await exerciseSessionReset(native)
})
