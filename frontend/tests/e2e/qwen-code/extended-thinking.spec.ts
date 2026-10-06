import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { expectMissingOptionGroup } from '../helpers/unsupportedConfiguration'
import { qwenTest } from '../qwen-fixtures'

qwenTest('proves the missing extended-thinking setting against the live catalog and a native tool', async ({ page, modelScript, leapmuxServer, authenticatedQwenWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedQwenWorkspace.workspaceId, provider: AgentProvider.QWEN_CODE }
  await expectMissingOptionGroup(context, { groupId: 'thinking', relatedProof: () => exerciseShellToolExecution(context, { includeFailure: false }) })
})
