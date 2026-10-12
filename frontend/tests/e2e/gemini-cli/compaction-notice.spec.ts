import { geminiTest } from '../gemini-fixtures'
import { expectNoCompactionNotice } from '../helpers/unsupportedCompaction'
import { exerciseNativeCompactCommandLimit } from './scenarios'

geminiTest('preserves the model context when the unsupported native compact command reaches the model', async ({ native }) => {
  await expectNoCompactionNotice(native, { relatedProof: () => exerciseNativeCompactCommandLimit(native) })
})
