import { geminiTest } from '../gemini-fixtures'
import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { relatedNativeProof } from './scenarios'

geminiTest('exposes no native setting for selectable reasoning effort', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'reasoning-effort', relatedProof: () => relatedNativeProof(native) })
})
