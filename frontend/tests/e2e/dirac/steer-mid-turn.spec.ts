import { DIRAC_AGENT, diracTest } from '../dirac-fixtures'
import { exerciseSteerBeforeTool } from '../helpers/nativeToolSteering'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { nativeContext } from './scenarios'

diracTest.describe('Dirac model and steering', () => {
  // Dirac takes the steering message as a native whisper into the active turn. Its answer goes through its respond
  // tool, which the context's text step builds.
  diracTest('puts a native whisper into the active turn', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, DIRAC_AGENT)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await exerciseSteerBeforeTool(context)
  })
})
