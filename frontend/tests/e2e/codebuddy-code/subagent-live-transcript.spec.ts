import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseLiveChildTranscript } from '../helpers/liveChildTranscript'

codebuddyTest.describe('CodeBuddy Code subagent registry', () => {
  const PROVIDER = AgentProvider.CODEBUDDY

  codebuddyTest('shows the child prompt while the child still runs', async ({ codebuddyWorkspace, page, modelScript }) => {
    await exerciseLiveChildTranscript(page, modelScript, {
      provider: PROVIDER,
      childWhen: { user: 'Reply with CHILD_LIVE_DONE' },
      childTask: 'Reply with CHILD_LIVE_DONE.',
      parentTask: 'Delegate the live child task.',
      toolProof: { workingDir: codebuddyWorkspace.workingDir },
    })
  })
})
