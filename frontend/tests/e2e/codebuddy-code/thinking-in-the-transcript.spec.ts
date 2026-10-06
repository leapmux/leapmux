import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseThinkingRows } from '../helpers/thinkingRows'

codebuddyTest.describe('CodeBuddy Code basic chat', () => {
  codebuddyTest('draws model reasoning in a thought row', async ({ native }) => {
    await exerciseThinkingRows(native)
  })
})
