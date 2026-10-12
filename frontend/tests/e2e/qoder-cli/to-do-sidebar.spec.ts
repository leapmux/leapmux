import { exerciseTodoListReplacement } from '../helpers/todoSidebar'
import { qoderTest } from '../qoder-fixtures'

qoderTest.describe('qoder CLI to-do sidebar', () => {
  qoderTest('shows native WriteTodos state and restores it after reload', async ({ native }) => {
    await exerciseTodoListReplacement(native)
  })
})
