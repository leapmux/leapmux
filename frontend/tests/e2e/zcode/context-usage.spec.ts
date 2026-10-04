import { exerciseContextUsage } from '../helpers/contextUsage'
import { ZCODE_E2E_SKIP_REASON, zcodeTest } from '../zcode-fixtures'

zcodeTest.skip(!!ZCODE_E2E_SKIP_REASON, ZCODE_E2E_SKIP_REASON || '')

zcodeTest('shows the context usage that the model reports', async ({ authenticatedZCodeWorkspace, page, modelScript }) => {
  void authenticatedZCodeWorkspace
  await exerciseContextUsage(page, modelScript)
})
