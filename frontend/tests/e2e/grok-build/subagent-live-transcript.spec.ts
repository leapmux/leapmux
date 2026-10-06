import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { GROK_AGENT, grokTest } from '../grok-fixtures'
import { exerciseLiveChildTranscript } from '../helpers/liveChildTranscript'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'

/**
 * An actual native child publishes messages before it ends. Its separate transcript tab must show those messages.
 *
 * The Worker drives Grok Build through the Agent Client Protocol.
 *
 * Grok sends child output through its _x.ai/session_notification extension. Its native subagent cancel stops a selected child.
 */
/**
 * The words that open the system prompt of a Grok subagent's own turn.
 *
 * A child session asks for a session title too, and that request carries the
 * child's prompt as well. Only the child's own turn states these words, so a
 * rule that requires them leaves the title to the housekeeping rule.
 */
const GROK_SUBAGENT_SYSTEM = 'You are a Grok Build subagent\\b'

grokTest.describe('Grok Build subagent registry', () => {
  grokTest('shows the child prompt while the child still runs', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, GROK_AGENT, { optionValues: { approvalMode: 'always-approve' } })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await exerciseLiveChildTranscript({ page, modelScript, leapmuxServer, provider: AgentProvider.GROK_BUILD }, {
      childWhen: { system: GROK_SUBAGENT_SYSTEM, user: 'Reply with CHILD_LIVE_DONE' },
      childTask: 'Reply with CHILD_LIVE_DONE.',
      parentTask: 'Delegate the live child task.',
      toolProof: { read: { workingDir } },
    })
  })
})
