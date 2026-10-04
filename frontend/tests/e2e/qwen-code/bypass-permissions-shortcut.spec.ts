import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'
import { QWEN_E2E_SKIP_REASON, qwenTest } from '../qwen-fixtures'

qwenTest.skip(!!QWEN_E2E_SKIP_REASON, QWEN_E2E_SKIP_REASON || '')

qwenTest('applies native Bypass before and after reload without a permission prompt', async ({ page, modelScript, leapmuxServer, authenticatedQwenWorkspace }) => {
  await exerciseBypassPermissions({ page, modelScript, leapmuxServer, workspaceId: authenticatedQwenWorkspace.workspaceId, provider: AgentProvider.QWEN_CODE })
})
