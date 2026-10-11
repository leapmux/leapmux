import { exerciseCloseAgent } from '../helpers/nativeLifecycle'
import { ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest('closes the UI tab and waits for owned process exit and Worker close', async ({ native }) => {
  await exerciseCloseAgent(native)
})
