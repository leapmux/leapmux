import { droidTest } from '../droid-fixtures'
import { exerciseTodoListReplacement } from '../helpers/todoSidebar'

droidTest.describe('factory Droid to-do sidebar', () => {
  droidTest('shows native TodoWrite state and restores it after reload', async ({ native }) => {
    await exerciseTodoListReplacement(native)
  })
})
