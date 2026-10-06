import { exerciseSteerAfterTool } from '../helpers/nativeToolSteering'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { kiroTest } from '../kiro-fixtures'
import { KIRO_AGENT, nativeContext } from './scenarios'

kiroTest.describe('Kiro interrupt, steering and process lifetime', () => {
  // The allow-all preset runs the command with no permission banner. Kiro counts its live progress in tokens
  // (`kiro/generation-progress.spec.ts`), and no spec proves that it draws a running command's output, so the
  // proof reads the output from the model request alone.
  kiroTest('steers a running turn with a queued message', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, KIRO_AGENT, { optionValues: { policyPreset: 'allow-all' } })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await exerciseSteerAfterTool(context, { expectDisplayedOutput: false })
  })
})
