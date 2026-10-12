import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { expectMissingPermissionShortcut } from '../helpers/unsupportedConfiguration'

deepseekHarnessTest('proves the native default presets provide no Smart shortcut', async ({ native }) => {
  await expectMissingPermissionShortcut(native, { preset: 'smart', relatedProof: () => exerciseShellToolExecution(native, { includeFailure: false }) })
})
