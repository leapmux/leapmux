import { grokTest } from '../grok-fixtures'
import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'

grokTest('applies native Bypass before and after reload without a permission prompt', async ({ native }) => {
  await exerciseBypassPermissions(native)
})
