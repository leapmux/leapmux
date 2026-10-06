import { expectNoCompactionNotice } from '../helpers/unsupportedCompaction'
import { lettaTest } from '../letta-fixtures'
import { exerciseOrdinaryCompactText } from './compactionScenarios'

lettaTest('receives no completed compaction notice from the actual native command', async ({ native }) => {
  await expectNoCompactionNotice(native, { relatedProof: () => exerciseOrdinaryCompactText(native) })
})
