import { expectNoCompactionNotice } from '../helpers/unsupportedCompaction'
import { junieTest } from '../junie-fixtures'
import { exerciseCompressAcknowledgement } from './compactionScenarios'

junieTest('receives no completed compaction notice from the actual native command', async ({ native }) => {
  await expectNoCompactionNotice(native, { relatedProof: () => exerciseCompressAcknowledgement(native) })
})
