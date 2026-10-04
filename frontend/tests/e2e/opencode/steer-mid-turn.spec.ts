import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseProviderSteer } from '../helpers/providerSteer'
import { OPENCODE_E2E_SKIP_REASON, opencodeTest } from '../opencode-fixtures'

opencodeTest.skip(!!OPENCODE_E2E_SKIP_REASON, OPENCODE_E2E_SKIP_REASON || '')

opencodeTest('places queued guidance in the next native model request', async ({ authenticatedOpencodeWorkspace, page, modelScript }) => {
  void authenticatedOpencodeWorkspace
  await exerciseProviderSteer(page, modelScript, AgentProvider.OPENCODE)
})
