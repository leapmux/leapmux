import { exerciseCompactAsModelText } from '../helpers/unsupportedCompaction'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('passes the slash command to the model in ACP mode', async ({ native }) => {
  await exerciseCompactAsModelText(native)
})
