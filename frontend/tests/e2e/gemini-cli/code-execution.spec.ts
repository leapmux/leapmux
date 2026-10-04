import { expect } from '@playwright/test'
import { GEMINI_E2E_SKIP_REASON, geminiTest } from '../gemini-fixtures'
import { expectNativeCodeExecutionAbsent } from '../helpers/nativeCodeExecution'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeModelToolNames } from '../helpers/nativeScenario'
import { nativeContext } from './scenarios'
import { exerciseGeminiShellToolExecution } from './shellScenarios'

geminiTest.skip(!!GEMINI_E2E_SKIP_REASON, GEMINI_E2E_SKIP_REASON || '')

geminiTest('excludes a native script executor while a real shell command still executes', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  const catalog = await sendNativeAnswer(context, 'Return the native executor catalog.', 'The native executor catalog reached the mock.')
  expect(nativeModelToolNames(catalog)).toContain('run_shell_command')
  expectNativeCodeExecutionAbsent(catalog, ['codemode', 'execute_code', 'run_code', 'execute_script'])
  await exerciseGeminiShellToolExecution(context)
})
