import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { OH_MY_PI_E2E_SKIP_REASON, ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest.skip(!!OH_MY_PI_E2E_SKIP_REASON, OH_MY_PI_E2E_SKIP_REASON || '')

ohMyPiTest('reopens the native picker handle and restores the saved transcript', async ({ page, modelScript, leapmuxServer, authenticatedOhMyPiWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedOhMyPiWorkspace.workspaceId, provider: AgentProvider.OH_MY_PI }
  await exerciseSessionResume(context)
})
