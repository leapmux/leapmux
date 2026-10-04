import { GOOSE_E2E_SKIP_REASON, gooseTest } from '../goose-fixtures'
import { exerciseTextGoalQueue } from '../helpers/subagentRegistry'

gooseTest.skip(!!GOOSE_E2E_SKIP_REASON, GOOSE_E2E_SKIP_REASON || '')

gooseTest('queues and observes session-goal commands', async ({
  authenticatedGooseWorkspace,
  page,
}) => {
  void authenticatedGooseWorkspace
  await exerciseTextGoalQueue(page, {
    objective: 'Wait for the Goose goal route unlock.',
    clearCommand: '/goal off',
  })
})
