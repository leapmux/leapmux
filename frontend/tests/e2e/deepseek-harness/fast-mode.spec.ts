import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { expectMissingOptionGroup } from '../helpers/unsupportedConfiguration'
import { nativeContext } from './scenarios'

deepseekHarnessTest('proves the missing fast-mode setting against native options and a real native tool', async ({ deepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: deepseekHarnessWorkspace.workspaceId })
  await expectMissingOptionGroup(context, { groupId: 'fastMode', relatedProof: () => exerciseShellToolExecution(context, { includeFailure: false }) })
})
