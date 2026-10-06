import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { qwenTest } from '../qwen-fixtures'
import { relatedNativeProof } from './scenarios'

qwenTest('proves the missing output-style setting against the live catalog and a native tool', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'output-style', relatedProof: () => relatedNativeProof(native) })
})
