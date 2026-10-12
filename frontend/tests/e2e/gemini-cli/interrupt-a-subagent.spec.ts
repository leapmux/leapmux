import { geminiTest } from '../gemini-fixtures'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { openGeminiRunningChild } from './childScenarios'

geminiTest('refuses the unsupported native child interrupt route while the child still runs', async ({ native }) => {
  await expectUnsupportedSubagent(native, { operation: 'interrupt', openChild: () => openGeminiRunningChild(native) })
})
