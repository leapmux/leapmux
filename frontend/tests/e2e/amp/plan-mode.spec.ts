import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { AMP_E2E_SKIP_REASON, ampTest } from '../amp-fixtures'
import { ampToolResultReader } from '../helpers/ampToolResult'
import { exerciseMissingNativePlanMode } from '../helpers/unsupportedPlanMode'

ampTest.skip(!!AMP_E2E_SKIP_REASON, AMP_E2E_SKIP_REASON || '')

ampTest('keeps the native plan command as text and offers no Plan mode', async ({ page, modelScript, leapmuxServer, authenticatedAmpWorkspace }) => {
  const context: ManagedNativeScenarioContext = { page, modelScript, leapmuxServer, workspaceId: authenticatedAmpWorkspace.workspaceId, provider: AgentProvider.AMP }
  context.readToolResult = ampToolResultReader(context)
  await exerciseMissingNativePlanMode(context)
})
