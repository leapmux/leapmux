import { GOOSE_E2E_SKIP_REASON, gooseTest } from '../goose-fixtures'
import { exerciseGooseCompaction } from './compactionScenario'

gooseTest.skip(!!GOOSE_E2E_SKIP_REASON, GOOSE_E2E_SKIP_REASON || '')

gooseTest('runs the native slash command and removes old context', async ({ authenticatedGooseWorkspace, page, modelScript }) => {
  void authenticatedGooseWorkspace
  await exerciseGooseCompaction({ page, modelScript })
})
