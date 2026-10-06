import { fastAgentTest } from '../fastagent-fixtures'
import { expectNoPlanOption } from '../helpers/unsupportedPlanMode'

fastAgentTest('exposes no native plan option', async ({ native }) => {
  await expectNoPlanOption(native)
})
