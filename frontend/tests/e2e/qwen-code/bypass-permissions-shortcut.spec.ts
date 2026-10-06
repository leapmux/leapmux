import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'
import { qwenTest } from '../qwen-fixtures'

qwenTest('applies native Bypass before and after reload without a permission prompt', async ({ native }) => {
  await exerciseBypassPermissions(native)
})
