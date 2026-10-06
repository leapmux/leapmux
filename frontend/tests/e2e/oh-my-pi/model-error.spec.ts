import { exerciseModelError } from '../helpers/nativeModelError'
import { ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest('shows the native model failure and accepts the next valid prompt', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})
