import { expectCompactionNoticeAfterReload } from '../helpers/compaction'
import { ohMyPiTest } from '../ohmypi-fixtures'
import { exerciseOhMyPiCompaction } from './compactionScenario'

ohMyPiTest('preserves the native completed compaction notice after a verified summary and reload', async ({ native }) => {
  await exerciseOhMyPiCompaction(native)
  await expectCompactionNoticeAfterReload(native.page)
})
