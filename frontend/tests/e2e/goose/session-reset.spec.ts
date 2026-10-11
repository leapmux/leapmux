import { gooseTest } from '../goose-fixtures'
import { exerciseSessionReset } from '../helpers/nativeLifecycle'

gooseTest('clears native context without discarding saved Worker messages', async ({ native }) => {
  await exerciseSessionReset(native)
})
