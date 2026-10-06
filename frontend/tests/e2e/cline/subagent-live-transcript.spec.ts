import { clineTest } from '../cline-fixtures'
import { exerciseLiveChildTranscript } from '../helpers/liveChildTranscript'

/**
 * An actual native child publishes messages before it ends. Its separate transcript tab must show those messages.
 *
 * The Worker starts one private Cline hub for this agent. Cline's DeepSeek provider sends requests to the isolated mock.
 *
 * Cline omits the child ID from streamed spawn_agent output. One active spawn lets the Worker route that output. Concurrent spawns require the completed native child session store.
 */
clineTest.describe('Cline subagent registry', () => {
  clineTest('shows the child prompt while the child still runs', async ({ native, authenticatedClineWorkspace }) => {
    await exerciseLiveChildTranscript(native, {
      childWhen: { user: 'Reply with CHILD_LIVE_DONE' },
      childTask: 'Reply with CHILD_LIVE_DONE.',
      parentTask: 'Delegate the live child task.',
      toolProof: { read: { workingDir: authenticatedClineWorkspace.workingDir } },
    })
  })
})
