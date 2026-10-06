import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { gooseTest } from '../goose-fixtures'
import { exerciseRelatedTodo } from '../helpers/relatedTodoProof'
import { applyPermissionPreset } from '../helpers/ui'
import { expectMissingOptionGroup } from '../helpers/unsupportedConfiguration'

gooseTest('proves the native output-style limit after a real sidebar operation', async ({ authenticatedGooseWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedGooseWorkspace.workspaceId, provider: AgentProvider.GOOSE }
  const relatedProof = () => exerciseRelatedTodo(context, { prepare: () => applyPermissionPreset(page, 'bypass') })
  await expectMissingOptionGroup(context, { groupId: 'outputStyle', relatedProof })
})
