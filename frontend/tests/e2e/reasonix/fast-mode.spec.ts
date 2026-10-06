import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseRelatedTodo } from '../helpers/relatedTodoProof'
import { applyPermissionPreset } from '../helpers/ui'
import { expectMissingOptionGroup } from '../helpers/unsupportedConfiguration'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('proves the native fast-mode limit after a real sidebar operation', async ({ authenticatedReasonixWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedReasonixWorkspace.workspaceId, provider: AgentProvider.REASONIX }
  const relatedProof = () => exerciseRelatedTodo(context, { prepare: () => applyPermissionPreset(page, 'bypass') })
  await expectMissingOptionGroup(context, { groupId: 'fastMode', relatedProof })
})
