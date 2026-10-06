import { cursorTest } from '../cursor-fixtures'
import { expectMissingPermissionShortcut } from '../helpers/unsupportedConfiguration'
import { exerciseCursorRelatedTodo } from './scenarios'

cursorTest('proves the native bypass-permissions-shortcut limit after a real sidebar operation', async ({ native }) => {
  await expectMissingPermissionShortcut(native, { preset: 'bypass', relatedProof: () => exerciseCursorRelatedTodo(native) })
})
