import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { claudeTest } from '../claude-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { createTestDirectory } from '../helpers/runDirectory'
import { nativeContext } from './scenarios'

claudeTest.describe('Attachment Support', () => {
  claudeTest('a text file reaches the model', async ({ page, modelScript, leapmuxServer, authenticatedWorkspace }) => {
    const context = await nativeContext({
      page,
      modelScript,
      leapmuxServer,
      workspaceId: authenticatedWorkspace.workspaceId,
    })
    await exerciseAttachmentDelivery(context, 'text', 'notes.txt', { protocol: 'anthropic-messages' })
  })

  /**
   * A message longer than one channel frame (64 KiB) goes out in encrypted chunks, and the composer sends them in work
   * slices that yield to the event loop. About 2 MiB takes several slices. The Worker must reassemble the chunks in order,
   * so the model receives every numbered line once, in order.
   */
  claudeTest('a text file of several encrypted chunks reaches the model whole', async ({ page, modelScript, leapmuxServer, authenticatedWorkspace }) => {
    const context = await nativeContext({
      page,
      modelScript,
      leapmuxServer,
      workspaceId: authenticatedWorkspace.workspaceId,
    })
    const fileName = 'chunked-notes.txt'
    const fixturePath = join(createTestDirectory('leapmux-chunked-text-'), fileName)
    const lines = Array.from({ length: 56 * 1024 }, (_, index) => `line ${String(index).padStart(6, '0')} of the chunked attachment`)
    writeFileSync(fixturePath, `${lines.join('\n')}\n`)
    await exerciseAttachmentDelivery(context, 'text', fileName, { protocol: 'anthropic-messages', fixturePath })
  })
})
