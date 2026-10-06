import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseLiveChildTranscript } from '../helpers/liveChildTranscript'

codebuddyTest.describe('CodeBuddy Code subagent registry', () => {
  codebuddyTest('shows the child prompt while the child still runs', async ({ native, authenticatedCodebuddyWorkspace }) => {
    await exerciseLiveChildTranscript(native, {
      childWhen: { user: 'Reply with CHILD_LIVE_DONE' },
      childTask: 'Reply with CHILD_LIVE_DONE.',
      parentTask: 'Delegate the live child task.',
      toolProof: { read: { workingDir: authenticatedCodebuddyWorkspace.workingDir } },
    })
  })
})
