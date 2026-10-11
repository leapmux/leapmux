import { codewhaleTest } from '../codewhale-fixtures'
import { exerciseLiveChildTranscript } from '../helpers/liveChildTranscript'

/**
 * An actual native child publishes messages before it ends. Its separate transcript tab must show those messages.
 *
 * The Worker drives Codewhale's runtime API. Codewhale stores each thread and its tool results in its private native store.
 *
 * The agent tool returns a child ID at once. Codewhale omits child events from the parent stream. The Worker reads the child transcript and run record until the run ends.
 */
codewhaleTest.describe('Codewhale subagent registry', () => {
  codewhaleTest('shows the child prompt while the child remains open', async ({ native }) => {
    await exerciseLiveChildTranscript(native, {
      childWhen: { user: 'Reply with CHILD_LIVE_DONE' },
      childTask: 'Reply with CHILD_LIVE_DONE.',
      parentTask: 'Delegate the live child task.',
      holdParentAnswer: true,
      background: true,
      // A resumable native interruption keeps the child tab open as Paused.
      allowPaused: true,
    })
  })

  // The child reads its file with the native read tool, which the explore role runs without approval.
  // The Worker reads the read result from the child transcript file while the next child answer stays held.
  codewhaleTest('shows a native child file result only in the running child tab', async ({ native, authenticatedCodewhaleWorkspace }) => {
    const workingDir = authenticatedCodewhaleWorkspace.workingDir
    if (!workingDir)
      throw new Error('The live child file proof requires the working directory of the native agent.')
    await exerciseLiveChildTranscript(native, {
      childWhen: { user: 'Read the assigned file in the live child' },
      childTask: 'Read the assigned file in the live child.',
      parentTask: 'Delegate the live child file read.',
      toolProof: { read: { workingDir } },
      holdParentAnswer: true,
      background: true,
      // A resumable native interruption keeps the child tab open as Paused.
      allowPaused: true,
    })
  })
})
