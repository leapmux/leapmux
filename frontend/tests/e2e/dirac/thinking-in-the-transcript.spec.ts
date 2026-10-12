import { diracTest } from '../dirac-fixtures'
import { exerciseThinkingRows } from '../helpers/thinkingRows'

diracTest.describe('Dirac thinking and context usage', () => {
  diracTest('draws the reasoning in a thought band', async ({ native }) => {
    // The context answers through Dirac's respond tool, and the reasoning rides on that step.
    await exerciseThinkingRows(native)
  })
})
