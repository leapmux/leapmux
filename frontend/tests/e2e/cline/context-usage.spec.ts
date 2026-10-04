import { CLINE_E2E_SKIP_REASON, clineTest } from '../cline-fixtures'
import { exerciseContextUsage } from '../helpers/contextUsage'

/**
 * The native usage event must reach the agent info card. The test checks the reported count and its display.
 *
 * The Worker starts one private Cline hub for this agent. Cline's DeepSeek provider sends requests to the isolated mock.
 */
clineTest.skip(!!CLINE_E2E_SKIP_REASON, CLINE_E2E_SKIP_REASON || '')

clineTest.describe('Cline basic chat', () => {
  clineTest('reports model usage in the agent info card', async ({ authenticatedClineWorkspace, page, modelScript }) => {
    void authenticatedClineWorkspace
    await exerciseContextUsage(page, modelScript)
  })
})
