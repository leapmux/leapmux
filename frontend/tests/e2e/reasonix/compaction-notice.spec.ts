import { exerciseCompactAsModelText, expectNoCompactionNotice } from '../helpers/unsupportedCompaction'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('retains native context without a compaction notice after the slash command', async ({ native }) => {
  await expectNoCompactionNotice(native, { relatedProof: () => exerciseCompactAsModelText(native) })
})
