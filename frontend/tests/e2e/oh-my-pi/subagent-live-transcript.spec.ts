import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseLiveChildTranscript } from '../helpers/liveChildTranscript'
import { ohMyPiYieldToolCall } from '../helpers/providerToolCalls'
import { OH_MY_PI_E2E_SKIP_REASON, ohMyPiTest } from '../ohmypi-fixtures'

/**
 * An actual native child publishes messages before it ends. Its separate transcript tab must show those messages.
 *
 * The Worker drives `omp --mode rpc-ui` through its JSON Lines protocol.
 *
 * Each task entry starts a separate child. Native subagent events identify its messages and progress. The yield tool ends that child. The test profile disables background tasks, so the parent waits for the child.
 */
ohMyPiTest.skip(!!OH_MY_PI_E2E_SKIP_REASON, OH_MY_PI_E2E_SKIP_REASON || '')

ohMyPiTest.describe('Oh My Pi subagent registry', () => {
  ohMyPiTest('shows the child prompt while the child still runs', async ({ authenticatedOhMyPiWorkspace, page, modelScript }) => {
    void authenticatedOhMyPiWorkspace
    await exerciseLiveChildTranscript(page, modelScript, {
      provider: AgentProvider.OH_MY_PI,
      childWhen: { user: 'Complete assignment thoroughly', body: '"name":"yield"' },
      childTask: 'Reply with CHILD_LIVE_DONE.',
      parentTask: 'Delegate the live child task.',
      childResponse: { toolCalls: [ohMyPiYieldToolCall('yield-live-report', 'CHILD_LIVE_DONE')] },
    })
  })
})
