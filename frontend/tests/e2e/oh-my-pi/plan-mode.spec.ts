import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseMissingNativePlanMode } from '../helpers/unsupportedPlanMode'
import { OH_MY_PI_E2E_SKIP_REASON, ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest.skip(!!OH_MY_PI_E2E_SKIP_REASON, OH_MY_PI_E2E_SKIP_REASON || '')

ohMyPiTest('keeps the native plan command as text and offers no Plan mode', async ({ page, modelScript, leapmuxServer, authenticatedOhMyPiWorkspace }) => {
  const context: ManagedNativeScenarioContext = { page, modelScript, leapmuxServer, workspaceId: authenticatedOhMyPiWorkspace.workspaceId, provider: AgentProvider.OH_MY_PI }
  await exerciseMissingNativePlanMode(context)
})
