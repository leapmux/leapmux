import { expect } from '@playwright/test'
import { ampTest } from '../amp-fixtures'
import { exerciseNativeCodeExecution, nativeJavaScriptTextCases, openNativeCatalogTurn } from '../helpers/nativeCodeExecution'
import { readAmpExecutorCatalog } from './nativeCatalog'
import { ampCatalogDiagnosticAttachment } from './nativeCatalogDiagnostic'
import { nativeContext } from './scenarios'

// The local catalog cannot establish whether Amp's server executes code_exec.
// Native remote execution evidence: https://github.com/leapmux/leapmux/issues/509.
ampTest('distinguishes the local executor catalog from server-side code execution', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  const context = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  })
  await openNativeCatalogTurn(context)
  const { tools } = await readAmpExecutorCatalog(context, {
    onOwnershipDiagnostic: ampCatalogDiagnosticAttachment(testInfo),
  })
  expect(tools).toContain('shell_command')
  expect(tools.filter(name => /^(?:codemode|exec|eval|REPL|js_execution|code_execution|execute_tools|node_repl|code_exec)$/.test(name))).toEqual([])
})

ampTest('retains remote code output and errors through the native client after reload', async ({ native }) => {
  // Real service probes establish execution. The isolated service computes these fixture results.
  await exerciseNativeCodeExecution(native, {
    scripts: nativeJavaScriptTextCases,
  })
})
