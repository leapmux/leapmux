import { expect } from '@playwright/test'
import { antigravityTest } from '../antigravity-fixtures'
import { expectNativeCodeExecutionAbsent } from '../helpers/nativeCodeExecution'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeModelToolNames } from '../helpers/nativeScenario'
import { exerciseAntigravityShellToolExecution } from './shellScenarios'

antigravityTest('excludes a native script executor while a real shell command still executes', async ({ native }) => {
  const catalog = await sendNativeAnswer(native, 'Return the native executor catalog.', 'The native executor catalog reached the mock.')
  expect(nativeModelToolNames(catalog)).toContain('run_command')
  expectNativeCodeExecutionAbsent(catalog, [
    'codemode',
    'execute_code',
    'run_code',
    'execute_script',
  ])
  await exerciseAntigravityShellToolExecution(native)
})
