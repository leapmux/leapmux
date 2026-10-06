import { ampTest } from '../amp-fixtures'
import { exerciseCompactAsModelText } from '../helpers/unsupportedCompaction'

ampTest.describe('Amp manual compaction', () => {
  ampTest('passes the slash command to the model in stream mode', async ({ native }) => {
    await exerciseCompactAsModelText(native)
  })
})
