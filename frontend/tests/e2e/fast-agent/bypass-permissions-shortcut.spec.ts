import { fastAgentTest } from '../fastagent-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { expectMissingPermissionShortcut } from '../helpers/unsupportedConfiguration'

fastAgentTest('exposes no bypass permission shortcut after actual native tool execution', async ({ native }) => {
  await expectMissingPermissionShortcut(native, { preset: 'bypass', relatedProof: () => exerciseShellToolExecution(native, { includeFailure: false }) })
})
