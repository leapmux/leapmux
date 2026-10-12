import { fastAgentTest } from '../fastagent-fixtures'
import { expectNoCompactionNotice } from '../helpers/unsupportedCompaction'
import { exerciseCompactRefusal } from './compactionScenarios'

fastAgentTest('receives no completed compaction notice from the actual native command', async ({ native }) => {
  await expectNoCompactionNotice(native, { relatedProof: () => exerciseCompactRefusal(native) })
})
