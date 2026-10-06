import { exerciseThinkingRows } from '../helpers/thinkingRows'
import { qoderTest } from '../qoder-fixtures'

qoderTest.describe('Qoder CLI basic chat', () => {
  qoderTest('draws model reasoning in a thought band', async ({ native }) => {
    await exerciseThinkingRows(native)
  })
})
