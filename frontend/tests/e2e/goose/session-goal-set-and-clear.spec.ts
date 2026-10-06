import { gooseTest } from '../goose-fixtures'
import { exerciseTextGoalQueue } from '../helpers/goalsAndTodos'

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
