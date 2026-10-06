import { commandCodeTest } from '../command-code-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { expectMissingOptionGroup } from '../helpers/unsupportedConfiguration'
import { nativeContext } from './scenarios'

commandCodeTest('proves the missing output-style setting against the live catalog and a native tool', async ({ authenticatedCommandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedCommandCodeWorkspace.workspaceId })
  await expectMissingOptionGroup(context, { groupId: 'outputStyle', relatedProof: () => exerciseShellToolExecution(context, { includeFailure: false }) })
})
