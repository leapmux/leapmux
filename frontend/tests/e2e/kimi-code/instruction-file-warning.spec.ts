import { kimiTest } from '../kimi-fixtures'
import { exerciseKimiInstructionWarning, nativeContext } from './scenarios'

kimiTest('states the oversized instruction file warning once', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await exerciseKimiInstructionWarning(context)
})
