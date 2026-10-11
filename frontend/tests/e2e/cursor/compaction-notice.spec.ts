import { cursorTest } from '../cursor-fixtures'
import { expectNoCompactionNotice } from '../helpers/unsupportedCompaction'
import { exerciseCursorCompactAsText } from './compactionScenario'

cursorTest('forwards compact as native prompt text without a completed notice', async ({ native }) => {
  await expectNoCompactionNotice(native, { relatedProof: () => exerciseCursorCompactAsText(native) })
})
