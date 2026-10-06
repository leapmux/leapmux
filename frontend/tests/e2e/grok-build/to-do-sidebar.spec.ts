import { grokTest } from '../grok-fixtures'
import { exerciseRelatedTodo, expectRelatedTodoSurvivesReload } from '../helpers/relatedTodoProof'

grokTest('persists the actual native task snapshot in the sidebar after reload', async ({ native }) => {
  const item = 'Keep the native task snapshot'
  await exerciseRelatedTodo(native, { item })
  await expectRelatedTodoSurvivesReload(native, item)
})
