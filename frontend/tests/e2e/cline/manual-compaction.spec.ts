import { clineTest } from '../cline-fixtures'
import { exerciseCompactAsModelText } from '../helpers/unsupportedCompaction'

clineTest('sends the slash command as model text without a native compaction', async ({ native }) => {
  await exerciseCompactAsModelText(native)
})
