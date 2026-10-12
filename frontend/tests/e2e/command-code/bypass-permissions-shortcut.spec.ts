import { commandCodeTest } from '../command-code-fixtures'
import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'

commandCodeTest('applies native bypass before and after reload without a permission prompt', async ({ native }) => {
  await exerciseBypassPermissions(native)
})
