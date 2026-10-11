import { expectCompactionNotice } from '../helpers/compaction'
import { ohMyPiTest } from '../ohmypi-fixtures'
import { exerciseOhMyPiCompaction } from './compactionScenario'

ohMyPiTest.describe('Oh My Pi compaction notice', () => {
  ohMyPiTest('draws the native notice after a manual compaction', async ({ native }) => {
    await exerciseOhMyPiCompaction(native)
    await expectCompactionNotice(native.page)
  })
})
