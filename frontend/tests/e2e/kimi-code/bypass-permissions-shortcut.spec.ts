import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'
import { kimiTest } from '../kimi-fixtures'

kimiTest('applies native Bypass before and after reload without a permission prompt', async ({ native }) => {
  await exerciseBypassPermissions(native)
})
