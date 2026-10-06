import { Buffer } from 'node:buffer'
import { clineTest } from '../cline-fixtures'
import { openNativeCatalogTurn } from '../helpers/nativeCodeExecution'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { nativeContext } from './scenarios'
import { readClineCompleteCatalog } from './toolCatalog'

clineTest('checks the complete native builtin inventory and executes the actual shell tool', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await openNativeCatalogTurn(context)
  const catalog = await readClineCompleteCatalog(context, receipt => testInfo.attach('cline-native-catalog-command-receipt', { body: Buffer.from(JSON.stringify(receipt)), contentType: 'application/json' }))
  await testInfo.attach('cline-complete-native-registry', { body: Buffer.from(JSON.stringify(catalog)), contentType: 'application/json' })
  await exerciseShellToolExecution(context, { includeFailure: false })
})
