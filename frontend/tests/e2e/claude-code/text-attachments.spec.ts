import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
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
   * A message longer than one channel frame (64 KiB) goes out in encrypted chunks. The channel session worker encrypts
   * them, and the page puts each frame on the WebSocket in that order. The Worker must reassemble the chunks in order, so
   * the model receives every numbered line once, in order. The retry fingerprint of the message is hashed in a worker of
   * its own, so both workers must run in the built app.
   */
  claudeTest('delivers a text file of several encrypted chunks to the model whole', async ({ page, modelScript, leapmuxServer, authenticatedWorkspace }) => {
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

    // The fingerprint worker spawns at the first send, so this check follows the send.
    await expect.poll(() => page.workers().some(worker => worker.url().includes('fieldDigestWorker'))).toBe(true)
    await expect.poll(() => page.workers().some(worker => worker.url().includes('channelSessionWorker'))).toBe(true)
  })
})
