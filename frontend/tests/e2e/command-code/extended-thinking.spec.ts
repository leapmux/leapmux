import { commandCodeTest } from '../command-code-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { expectMissingOptionGroup } from '../helpers/unsupportedConfiguration'
import { nativeContext } from './scenarios'

commandCodeTest('proves the missing extended-thinking setting against the live catalog and a native tool', async ({ commandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: commandCodeWorkspace.workspaceId })
  await expectMissingOptionGroup(context, { groupId: 'thinking', relatedProof: () => exerciseShellToolExecution(context, { includeFailure: false }) })
})
