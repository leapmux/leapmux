import { expect } from '@playwright/test'
import { AMP_AGENT, ampTest } from '../amp-fixtures'
import { openNativeCatalogTurn } from '../helpers/nativeCodeExecution'
import { readAmpExecutorCatalog } from './nativeCatalog'
import { ampCatalogDiagnosticAttachment } from './nativeCatalogDiagnostic'
import { nativeContext } from './scenarios'

ampTest('confirms the native code executor is absent from the actual model catalog', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await openNativeCatalogTurn(context, AMP_AGENT)
  const { tools } = await readAmpExecutorCatalog(context, { onOwnershipDiagnostic: ampCatalogDiagnosticAttachment(testInfo) })
  expect(tools).toContain('shell_command')
  expect(tools.filter(name => /^(?:codemode|exec|eval|REPL|js_execution|code_execution|execute_tools|node_repl)$/.test(name))).toEqual([])
})
