import { piTodoToolCall } from '../helpers/providerToolCalls'
import { exerciseRelatedTodo, RELATED_TODO_CALL_ID, RELATED_TODO_ITEM } from '../helpers/relatedTodoProof'
import { expectMissingPermissionShortcut } from '../helpers/unsupportedConfiguration'
import { piTest } from '../pi-fixtures'

piTest('proves the native bypass-permissions-shortcut limit after a real sidebar operation', async ({ native }) => {
  // Pi has no update-todos call in the vocabulary, so the proof creates the item through its todo extension.
  const relatedProof = () => exerciseRelatedTodo(native, { toolCall: piTodoToolCall(RELATED_TODO_CALL_ID, { action: 'create', subject: RELATED_TODO_ITEM }) })
  await expectMissingPermissionShortcut(native, { preset: 'bypass', relatedProof })
})
