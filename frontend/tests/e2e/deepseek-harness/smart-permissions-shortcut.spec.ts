import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { expectMissingPermissionShortcut } from '../helpers/unsupportedConfiguration'
import { nativeContext } from './scenarios'

deepseekHarnessTest('proves the native default presets provide no Smart shortcut', async ({ deepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: deepseekHarnessWorkspace.workspaceId })
  await expectMissingPermissionShortcut(context, { preset: 'smart', relatedProof: () => exerciseShellToolExecution(context, { includeFailure: false }) })
})
