import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import { exerciseConversationContext } from '../helpers/nativeConversation'
import { expectMissingOptionGroup } from '../helpers/unsupportedConfiguration'

codexTest('keeps the native catalog and restored UI free of the unsupported output-style axis', async ({ authenticatedCodexWorkspace, page, leapmuxServer, modelScript }) => {
  const context = { page, modelScript, leapmuxServer, provider: AgentProvider.CODEX, workspaceId: authenticatedCodexWorkspace.workspaceId }
  await expectMissingOptionGroup(context, { groupId: 'outputStyle', relatedProof: () => exerciseConversationContext(context) })
})
