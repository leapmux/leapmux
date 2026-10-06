import { exerciseThinkingRows } from '../helpers/thinkingRows'
import { lettaTest } from '../letta-fixtures'
import { nativeContext } from './scenarios'

lettaTest.describe('Letta Code basic chat', () => {
  lettaTest('draws model reasoning in a thought band', async ({ authenticatedReasoningLettaWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedReasoningLettaWorkspace.workspaceId })
    await exerciseThinkingRows(context)
  })
})
