import { commandCodeTest } from '../command-code-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { expectMissingOptionGroup } from '../helpers/unsupportedConfiguration'
import { nativeContext } from './scenarios'

commandCodeTest('proves the missing swarm-mode setting against the live catalog and a native tool', async ({ commandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: commandCodeWorkspace.workspaceId })
  await expectMissingOptionGroup(context, { groupId: 'swarmMode', relatedProof: () => exerciseShellToolExecution(context, { includeFailure: false }) })
})
