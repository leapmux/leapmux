import { expectNoPlanOption } from '../helpers/unsupportedPlanMode'
import { lettaTest } from '../letta-fixtures'

lettaTest('exposes no native plan option', async ({ native }) => {
  await expectNoPlanOption(native)
})
