import { codexTest } from '../codex-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { expectMissingPermissionShortcut } from '../helpers/unsupportedConfiguration'
import { codexExecContext } from './scenarios'

codexTest('keeps the smart shortcut absent while the actual native tool path works', async ({ native }) => {
  // The Codex context reads a shell result through the exec output of Codex.
  const context = codexExecContext(native)
  await expectMissingPermissionShortcut(context, { preset: 'smart', relatedProof: () => exerciseShellToolExecution(context) })
})
