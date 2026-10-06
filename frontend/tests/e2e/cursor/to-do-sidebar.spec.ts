import { cursorTest } from '../cursor-fixtures'
import { exerciseTodoListReplacement } from '../helpers/todoSidebar'

cursorTest('the sidebar follows each list the agent writes, and keeps it after a reload', async ({ native }) => {
  // Cursor runs the to-do tool and answers in one model exchange.
  await exerciseTodoListReplacement(native, { answerStep: 'same-step' })
})
