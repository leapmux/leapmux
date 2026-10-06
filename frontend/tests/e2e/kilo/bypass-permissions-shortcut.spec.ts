import { exerciseRelatedTodo } from '../helpers/relatedTodoProof'
import { expectMissingPermissionShortcut } from '../helpers/unsupportedConfiguration'
import { kiloTest } from '../kilo-fixtures'

kiloTest('proves the native bypass-permissions-shortcut limit after a real sidebar operation', async ({ native }) => {
  await expectMissingPermissionShortcut(native, { preset: 'bypass', relatedProof: () => exerciseRelatedTodo(native) })
})
