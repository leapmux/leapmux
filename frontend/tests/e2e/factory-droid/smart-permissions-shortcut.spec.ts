import { droidTest } from '../droid-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { expectMissingPermissionShortcut } from '../helpers/unsupportedConfiguration'

droidTest('exposes no smart permission shortcut after actual native tool execution', async ({ native }) => {
  await expectMissingPermissionShortcut(native, { preset: 'smart', relatedProof: () => exerciseShellToolExecution(native, { includeFailure: false }) })
})
