import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CODEWHALE_E2E_SKIP_REASON, codewhaleTest } from '../codewhale-fixtures'
import { exerciseLiveChildTranscript } from '../helpers/liveChildTranscript'

/**
 * An actual native child publishes messages before it ends. Its separate transcript tab must show those messages.
 *
 * The Worker drives Codewhale's runtime API. Codewhale stores each thread and its tool results in its private native store.
 *
 * The agent tool returns a child ID at once. Codewhale omits child events from the parent stream. The Worker reads the child transcript and run record until the run ends.
 */
codewhaleTest.skip(!!CODEWHALE_E2E_SKIP_REASON, CODEWHALE_E2E_SKIP_REASON || '')

codewhaleTest.describe('Codewhale subagent registry', () => {
  codewhaleTest('shows the child prompt while the child remains open', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace
    await exerciseLiveChildTranscript(page, modelScript, {
      provider: AgentProvider.CODEWHALE,
      childWhen: { user: 'Reply with CHILD_LIVE_DONE' },
      childTask: 'Reply with CHILD_LIVE_DONE.',
      parentTask: 'Delegate the live child task.',
      holdParentAnswer: true,
      background: true,
      // A resumable native interruption keeps the child tab open as Paused.
      allowPaused: true,
    })
  })
})
