import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseProviderSteer } from '../helpers/providerSteer'
import { OH_MY_PI_E2E_SKIP_REASON, ohMyPiTest } from '../ohmypi-fixtures'

/**
 * The queued input enters the same native turn after the running tool. The next native request must carry the input.
 *
 * The Worker drives `omp --mode rpc-ui` through its JSON Lines protocol.
 */
ohMyPiTest.skip(!!OH_MY_PI_E2E_SKIP_REASON, OH_MY_PI_E2E_SKIP_REASON || '')

ohMyPiTest('steers a queued message into the active turn', async ({ authenticatedOhMyPiWorkspace, page, modelScript }) => {
  void authenticatedOhMyPiWorkspace
  await exerciseProviderSteer(page, modelScript, AgentProvider.OH_MY_PI)
})
