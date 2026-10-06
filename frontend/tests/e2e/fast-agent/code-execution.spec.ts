import { fastAgentTest } from '../fastagent-fixtures'
import { openNativeCatalogTurn } from '../helpers/nativeCodeExecution'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { nativeContext } from './scenarios'
import { assertFastAgentShellCatalog, readFastAgentCompleteCatalog } from './toolCatalog'

fastAgentTest('confirms the native code executor is absent from the actual model catalog', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await openNativeCatalogTurn(context)
  // The native command reads all runtime tools. Each following command supplies one exact input schema.
  assertFastAgentShellCatalog(await readFastAgentCompleteCatalog(context))
  await exerciseShellToolExecution(context, { includeFailure: false })
})
