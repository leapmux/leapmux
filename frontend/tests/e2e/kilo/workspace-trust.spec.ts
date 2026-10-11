import { exerciseNativeWorkspaceTrustLimit, instructionFileConfiguration, projectConfigurationWorker } from '../helpers/nativeWorkspaceTrustLimit'
import { kiloTest } from '../kilo-fixtures'
import { opencodeInstructionSource } from '../opencode/scenarios'
import { nativeContext, nativeLaunch } from './scenarios'

kiloTest('starts and reads a private project without a workspace trust request', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await exerciseNativeWorkspaceTrustLimit(context, {
    worker: projectConfigurationWorker(leapmuxServer.agentEnv, nativeLaunch(context), 'KILO_DISABLE_PROJECT_CONFIG'),
    // Kilo reads AGENTS.md from the working directory up to the root of its git repository.
    projectConfiguration: instructionFileConfiguration('AGENTS.md', { gitRoot: true, sourceLine: opencodeInstructionSource }),
  })
})
