import { exerciseNativeWorkspaceTrustLimit, instructionFileConfiguration, projectConfigurationWorker } from '../helpers/nativeWorkspaceTrustLimit'
import { opencodeTest } from '../opencode-fixtures'
import { nativeContext, opencodeInstructionSource } from './scenarios'

opencodeTest('starts and reads a private project without a workspace trust request', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await exerciseNativeWorkspaceTrustLimit(context, {
    worker: projectConfigurationWorker(leapmuxServer.agentEnv, { binaryName: 'opencode', holdWhen: ['acp'] }, 'OPENCODE_DISABLE_PROJECT_CONFIG'),
    // OpenCode reads AGENTS.md from the working directory up to the root of its git repository.
    projectConfiguration: instructionFileConfiguration('AGENTS.md', { gitRoot: true, sourceLine: opencodeInstructionSource }),
  })
})
