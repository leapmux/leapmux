import { commandCodeTest } from '../command-code-fixtures'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { nativeContext, nativeLaunch } from './scenarios'

commandCodeTest('delivers queued startup input and retains it after an actual startup failure', async ({ authenticatedCommandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedCommandCodeWorkspace.workspaceId })
  await exerciseAgentStartup(context, { launch: nativeLaunch(context) })
  await exerciseAgentStartup(context, { launch: nativeLaunch(context), failed: true })
})
