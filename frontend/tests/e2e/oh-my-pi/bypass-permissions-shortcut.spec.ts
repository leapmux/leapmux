import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'
import { OH_MY_PI_E2E_SKIP_REASON, ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest.skip(!!OH_MY_PI_E2E_SKIP_REASON, OH_MY_PI_E2E_SKIP_REASON || '')

ohMyPiTest('applies native Bypass before and after reload without a permission prompt', async ({ page, modelScript, leapmuxServer, authenticatedOhMyPiWorkspace }) => {
  await exerciseBypassPermissions({ page, modelScript, leapmuxServer, workspaceId: authenticatedOhMyPiWorkspace.workspaceId, provider: AgentProvider.OH_MY_PI })
})
