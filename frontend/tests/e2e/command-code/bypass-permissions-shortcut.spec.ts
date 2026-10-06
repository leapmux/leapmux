import { commandCodeTest } from '../command-code-fixtures'
import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'
import { nativeContext } from './scenarios'

commandCodeTest('applies native bypass before and after reload without a permission prompt', async ({ authenticatedCommandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedCommandCodeWorkspace.workspaceId })
  await exerciseBypassPermissions(context)
})
