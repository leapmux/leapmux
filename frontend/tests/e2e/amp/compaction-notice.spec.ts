import { ampTest } from '../amp-fixtures'
import { exerciseCompactAsModelText, expectNoCompactionNotice } from '../helpers/unsupportedCompaction'

ampTest('proves the native slash text path has no completed compaction notice', async ({ native }) => {
  await expectNoCompactionNotice(native, { relatedProof: () => exerciseCompactAsModelText(native) })
})
