import { exerciseLiveChildTranscript } from '../helpers/liveChildTranscript'
import { ohMyPiYieldToolCall } from '../helpers/providerToolCalls'
import { ohMyPiTest } from '../ohmypi-fixtures'
import { ohMyPiChildTurn } from './childScenario'

/**
 * An actual native child publishes messages before it ends. Its separate transcript tab must show those messages.
 *
 * The Worker drives `omp --mode rpc-ui` through its JSON Lines protocol.
 *
 * Each task entry starts a separate child. Native subagent events identify its messages and progress. The yield tool ends that child. The test profile disables background tasks, so the parent waits for the child.
 */
ohMyPiTest.describe('Oh My Pi subagent registry', () => {
  ohMyPiTest('shows the child prompt while the child still runs', async ({ native }) => {
    await exerciseLiveChildTranscript(native, {
      childWhen: ohMyPiChildTurn(),
      childTask: 'Reply with CHILD_LIVE_DONE.',
      parentTask: 'Delegate the live child task.',
      childResponse: { toolCalls: [ohMyPiYieldToolCall('yield-live-report', 'CHILD_LIVE_DONE')] },
    })
  })

  // The held answer after the read is the native yield call, which ends the child with no further model request.
  // A text answer would make Oh My Pi send a yield reminder, which is a model request that the script does not answer.
  ohMyPiTest('shows a native child file result only in the running child tab', async ({ native, authenticatedOhMyPiWorkspace }) => {
    const workingDir = authenticatedOhMyPiWorkspace.workingDir
    if (!workingDir)
      throw new Error('The live child file proof requires the working directory of the native agent.')
    await exerciseLiveChildTranscript(native, {
      childWhen: ohMyPiChildTurn(),
      childTask: 'Read the assigned file in the live child.',
      parentTask: 'Delegate the live child file read.',
      toolProof: { read: { workingDir } },
      childResponse: { toolCalls: [ohMyPiYieldToolCall('yield-live-read-report', 'CHILD_LIVE_DONE')] },
    })
  })
})
