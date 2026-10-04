import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { AMP_E2E_SKIP_REASON, ampTest } from '../amp-fixtures'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'

ampTest.skip(!!AMP_E2E_SKIP_REASON, AMP_E2E_SKIP_REASON || '')

ampTest('reopens the native picker handle and restores the saved transcript', async ({ page, modelScript, leapmuxServer, authenticatedAmpWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedAmpWorkspace.workspaceId, provider: AgentProvider.AMP }
  await exerciseSessionResume(context)
})
