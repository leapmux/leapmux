import { ampTest } from '../amp-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { expectMissingPermissionShortcut } from '../helpers/unsupportedConfiguration'

ampTest('proves the absent shortcut after a native shell operation', async ({ native }) => {
  await expectMissingPermissionShortcut(native, { preset: 'smart', relatedProof: () => exerciseShellToolExecution(native, { includeFailure: false }) })
})
