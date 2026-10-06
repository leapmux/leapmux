import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'
import { ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest('applies native Bypass before and after reload without a permission prompt', async ({ native }) => {
  await exerciseBypassPermissions(native)
})
