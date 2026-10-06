import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { cursorTest } from '../cursor-fixtures'

import { exerciseUngroupedNativeChildren } from '../helpers/ungroupedNativeChildren'
import { openCursorRunningChild } from './childScenario'

cursorTest('keeps two actual native children outside workflow groups after reload', async ({ authenticatedCursorWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCursorWorkspace.workspaceId, provider: AgentProvider.CURSOR }
  const suffix = crypto.randomUUID().replaceAll('-', '')
  await exerciseUngroupedNativeChildren(context, {
    openChild: index => openCursorRunningChild(context, {
      description: `Actual grouping child ${index} ${suffix}`,
      allowExistingRows: index > 0,
    }),
  })
})
