import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { JUNIE_E2E_SKIP_REASON, junieTest } from '../junie-fixtures'
import { exerciseNativePlanReview } from './planScenarios'

junieTest.describe('Junie plan review', () => {
  junieTest.skip(!!JUNIE_E2E_SKIP_REASON, JUNIE_E2E_SKIP_REASON || '')

  junieTest('a plan raises a review request and the plan entries in the to-do sidebar', async ({ authenticatedJunieWorkspace, page, modelScript }) => {
    void authenticatedJunieWorkspace
    await exerciseNativePlanReview({ page, modelScript, provider: AgentProvider.JUNIE })
  })
})
