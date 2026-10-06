import { exerciseRelatedTodo, expectRelatedTodoSurvivesReload } from '../helpers/relatedTodoProof'
import { qwenTest } from '../qwen-fixtures'

qwenTest('persists the actual native task snapshot in the sidebar after reload', async ({ native }) => {
  const item = 'Keep the native task snapshot'
  await exerciseRelatedTodo(native, { item })
  await expectRelatedTodoSurvivesReload(native, item)
})
