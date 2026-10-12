import { droidTest } from '../droid-fixtures'
import { exerciseThinkingRows } from '../helpers/thinkingRows'
import { nativeContext } from './scenarios'

droidTest.describe('Factory Droid basic chat', () => {
  droidTest('draws model reasoning in a thought band', async ({ authenticatedReasoningDroidWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedReasoningDroidWorkspace.workspaceId })
    await exerciseThinkingRows(context)
  })
})
