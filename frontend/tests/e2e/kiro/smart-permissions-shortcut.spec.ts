import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { expectMissingPermissionShortcut } from '../helpers/unsupportedConfiguration'
import { kiroTest } from '../kiro-fixtures'

kiroTest('proves the absent shortcut after a native shell operation', async ({ native }) => {
  await expectMissingPermissionShortcut(native, { preset: 'smart', relatedProof: () => exerciseShellToolExecution(native, { includeFailure: false }) })
})
