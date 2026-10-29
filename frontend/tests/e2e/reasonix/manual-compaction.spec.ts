import { reasonixTest } from '../reasonix-fixtures'
import { exerciseReasonixCompactAsModelText } from './compactionScenario'

reasonixTest('passes the slash command to the model in ACP mode', async ({ native }) => {
  await exerciseReasonixCompactAsModelText(native)
})
