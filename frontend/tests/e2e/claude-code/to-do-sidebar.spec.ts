import { claudeTest } from '../claude-fixtures'
import { exerciseTodoListReplacement } from '../helpers/todoSidebar'

claudeTest.describe('Claude Code to-do sidebar', () => {
  // TodoWrite sends the whole list again, so the second call replaces the first.
  // Claude suppresses the todo row in the transcript, so the sidebar is the surface
  // that the matrix documents.
  claudeTest('the sidebar follows each list the agent writes, and keeps it after a reload', async ({ native }) => {
    await exerciseTodoListReplacement(native)
  })
})
