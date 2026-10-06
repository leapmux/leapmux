import { gooseTest } from '../goose-fixtures'
import { exerciseTextGoalQueue } from '../helpers/goalsAndTodos'

gooseTest('queues and observes session-goal commands', async ({ native }) => {
  await exerciseTextGoalQueue(native, {
    objective: 'Wait for the Goose goal route unlock.',
    clearCommand: '/goal off',
  })
})
