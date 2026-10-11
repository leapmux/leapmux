import { copilotTest } from '../copilot-fixtures'
import { exerciseNativeWorkspaceTrustLimit, instructionFileConfiguration } from '../helpers/nativeWorkspaceTrustLimit'
import { nativeContext } from './scenarios'

copilotTest('starts and reads a private project without a workspace trust request', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await exerciseNativeWorkspaceTrustLimit(context, { projectConfiguration: instructionFileConfiguration('AGENTS.md') })
})
