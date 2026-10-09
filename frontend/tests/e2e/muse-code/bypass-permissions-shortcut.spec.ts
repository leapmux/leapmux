import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'
import { museTest } from '../muse-fixtures'

museTest('applies native allowAll before and after reload', async ({ native }) => {
  await exerciseBypassPermissions(native)
})
