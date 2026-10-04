import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { AMP_E2E_SKIP_REASON, ampTest } from '../amp-fixtures'
import { exerciseModelError } from '../helpers/nativeModelError'

ampTest.skip(!!AMP_E2E_SKIP_REASON, AMP_E2E_SKIP_REASON || '')

ampTest('shows the native model failure and accepts the next valid prompt', async ({ page, modelScript, leapmuxServer, authenticatedAmpWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedAmpWorkspace.workspaceId, provider: AgentProvider.AMP }
  await exerciseModelError(context)
})
