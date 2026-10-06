import { clineTest } from '../cline-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { expectMissingPermissionShortcut } from '../helpers/unsupportedConfiguration'

clineTest('proves the absent shortcut after a native shell operation', async ({ native }) => {
  await expectMissingPermissionShortcut(native, { preset: 'smart', relatedProof: () => exerciseShellToolExecution(native, { includeFailure: false }) })
})
