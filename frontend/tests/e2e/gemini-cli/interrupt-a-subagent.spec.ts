import { GEMINI_E2E_SKIP_REASON, geminiTest } from '../gemini-fixtures'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { openGeminiRunningChild } from './childScenarios'
import { nativeContext } from './scenarios'

geminiTest.skip(!!GEMINI_E2E_SKIP_REASON, GEMINI_E2E_SKIP_REASON || '')

geminiTest('refuses the unsupported native child interrupt route while the child still runs', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  await expectUnsupportedSubagent(context, { operation: 'interrupt', openChild: () => openGeminiRunningChild(context) })
})
