import { grokTest } from '../grok-fixtures'
import { exerciseAllowThenFeedbackRejection } from '../helpers/nativePermission'
import { expectSettingsOptionChosen, openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { GROK_AGENT, nativeContext } from './scenarios'

grokTest.describe('Grok Build control requests', () => {
  // Grok's `ask` mode asks before a shell command writes a file. It permits `touch` and `mkdir`, so the commands of
  // `exerciseAllowThenFeedbackRejection` redirect their output.
  // An empty rejection ends the turn. A rejection with a reason puts that reason in native `followup_message`.
  // The same turn then continues.
  grokTest('approves one command and rejects the next with a reason the turn reads', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, GROK_AGENT)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectSettingsOptionChosen(page, 'approvalMode-ask')
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await exerciseAllowThenFeedbackRejection(context, { workingDir })
  })
})
