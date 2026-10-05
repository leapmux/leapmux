import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseLiveChildTranscript } from '../helpers/liveChildTranscript'
/**
 * An actual native child publishes messages before it ends. Its separate transcript tab must show those messages.
 *
 * The Worker drives MiMo Code's native HTTP server. MiMo identifies each child actor in its events.
 *
 * MiMo tags each child message with its actor ID. The Worker routes those messages into that child's transcript.
 */
import { MIMO_E2E_SKIP_REASON, mimoTest } from '../mimo-fixtures'

mimoTest.skip(!!MIMO_E2E_SKIP_REASON, MIMO_E2E_SKIP_REASON || '')

mimoTest.describe('MiMo Code subagent registry', () => {
  mimoTest('shows the child prompt while the child still runs', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    await exerciseLiveChildTranscript(page, modelScript, {
      provider: AgentProvider.MIMO_CODE,
      childWhen: { user: '^Reply with CHILD_LIVE_DONE' },
      childTask: 'Reply with CHILD_LIVE_DONE.',
      parentTask: 'Delegate the live child task.',
    })
  })

  // The general actor reads its file with the native read tool, which the default read rule allows.
  // MiMo tags the read with the actor ID of the child, so the parent transcript never holds it.
  mimoTest('shows a native child file result only in the running child tab', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    const workingDir = authenticatedMiMoWorkspace.workingDir
    if (!workingDir)
      throw new Error('The live child file proof requires the working directory of the native agent.')
    await exerciseLiveChildTranscript(page, modelScript, {
      provider: AgentProvider.MIMO_CODE,
      childWhen: { user: '^Read the assigned file in the live child' },
      childTask: 'Read the assigned file in the live child.',
      parentTask: 'Delegate the live child file read.',
      toolProof: { workingDir },
    })
  })
})
