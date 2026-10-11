import { claudeTest } from '../claude-fixtures'
import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'
import { nativeContext } from './scenarios'

claudeTest.describe('Attachment Support', () => {
  // The Linux/WebKitGTK image-paste path (entirely empty DataTransfer →
  // OS clipboard read via the Tauri clipboard-manager plugin) cannot be
  // exercised in headless Chromium: the bug is that WebKitGTK does not
  // populate DataTransfer at all, and Chromium does not reproduce that
  // shape. The conversion logic is covered by the platformBridge unit
  // test; manual paste in the desktop build is the only true end-to-end.

  claudeTest('unsupported file type stays out of the next model request', async ({ page, modelScript, leapmuxServer, authenticatedWorkspace }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedWorkspace.workspaceId })
    // The composer of the default provider refuses a binary file with no pill and a toast.
    await exerciseAttachmentRefusal(context, 'binary')
  })
})
