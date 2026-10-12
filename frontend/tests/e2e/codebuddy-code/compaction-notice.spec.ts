import { codebuddyTest } from '../codebuddy-fixtures'
import { expectNoCompactionNotice } from '../helpers/unsupportedCompaction'
import { exerciseContextCompactionWithoutNotice } from './compactionScenarios'

codebuddyTest('receives no completed compaction notice from the actual native command', async ({ native }) => {
  await expectNoCompactionNotice(native, { relatedProof: () => exerciseContextCompactionWithoutNotice(native) })
})
