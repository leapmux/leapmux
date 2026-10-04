import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CLINE_E2E_SKIP_REASON, clineTest } from '../cline-fixtures'
import { exerciseModelError } from '../helpers/nativeModelError'

clineTest.skip(!!CLINE_E2E_SKIP_REASON, CLINE_E2E_SKIP_REASON || '')

clineTest('shows the native model failure and accepts the next valid prompt', async ({ page, modelScript, leapmuxServer, authenticatedClineWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedClineWorkspace.workspaceId, provider: AgentProvider.CLINE }
  await exerciseModelError(context)
})
