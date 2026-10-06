import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { relatedNativeProof } from './scenarios'

deepseekHarnessTest('proves the missing fast-mode setting against native options and a real native tool', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'fast-mode', relatedProof: () => relatedNativeProof(native) })
})
