import { exerciseNativePermissionWrite } from '../helpers/nativePermission'
import { openWorkspace } from '../helpers/ui'
import { expectNoNativeEditorRequest } from '../helpers/unsupportedEditor'
import { openProviderAgent } from '../helpers/workspace'
import { JUNIE_AGENT, junieTest } from '../junie-fixtures'
import { nativeContext } from './scenarios'

junieTest('resolves an actual native permission without a multiline editor request', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, JUNIE_AGENT, { optionValues: { brave_mode: 'off' } })
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await expectNoNativeEditorRequest(context, { relatedProof: () => exerciseNativePermissionWrite(context) })
})
