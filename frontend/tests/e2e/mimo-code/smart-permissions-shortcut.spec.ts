import { expectMissingPermissionShortcut } from '../helpers/unsupportedConfiguration'
import { mimoTest } from '../mimo-fixtures'
import { exerciseMiMoShellToolExecution } from './shellToolExecution'

mimoTest('proves the absent shortcut after a native shell operation', async ({ native }) => {
  await expectMissingPermissionShortcut(native, { preset: 'smart', relatedProof: () => exerciseMiMoShellToolExecution(native, { includeFailure: false }) })
})
