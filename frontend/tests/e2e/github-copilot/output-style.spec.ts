import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { copilotTest } from '../copilot-fixtures'
import { exerciseRelatedTodo } from '../helpers/relatedTodoProof'
import { applyPermissionPreset } from '../helpers/ui'
import { expectMissingOptionGroup } from '../helpers/unsupportedConfiguration'

copilotTest('proves the native output-style limit after a real sidebar operation', async ({ authenticatedCopilotWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCopilotWorkspace.workspaceId, provider: AgentProvider.GITHUB_COPILOT }
  const relatedProof = () => exerciseRelatedTodo(context, { prepare: () => applyPermissionPreset(page, 'bypass') })
  await expectMissingOptionGroup(context, { groupId: 'outputStyle', relatedProof })
})
