import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { cursorTest } from '../cursor-fixtures'
import { expectMissingPermissionShortcut } from '../helpers/unsupportedConfiguration'
import { exerciseCursorRelatedTodo } from './scenarios'

cursorTest('proves the native smart-permissions-shortcut limit after a real sidebar operation', async ({ authenticatedCursorWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCursorWorkspace.workspaceId, provider: AgentProvider.CURSOR }
  const relatedProof = () => exerciseCursorRelatedTodo(context)
  await expectMissingPermissionShortcut(context, { preset: 'smart', relatedProof })
})
