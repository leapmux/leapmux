import type { OfficeDocumentKind } from '../helpers/officeAttachments'
import { basename } from 'node:path'
import { expect } from '@playwright/test'
import { exerciseAttachmentDelivery, exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'
import { writeOfficeAttachmentFixture } from '../helpers/officeAttachments'
import { createTestDirectory } from '../helpers/runDirectory'
import { userBubbles } from '../helpers/ui'
import { kiroTest } from '../kiro-fixtures'

kiroTest.describe('attachments for Kiro', () => {
  kiroTest('refuses a binary file', async ({ native }) => {
    await exerciseAttachmentRefusal(native, 'binary')
  })

  kiroTest('delivers Word and Excel documents and their updated bytes without a prompt', async ({ native }) => {
    const directory = createTestDirectory('kiro-office-')
    const documents: { kind: OfficeDocumentKind, mediaType: string }[] = [
      { kind: 'word', mediaType: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document' },
      { kind: 'spreadsheet', mediaType: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet' },
    ]
    const names = new Set<string>()
    for (const updated of [false, true]) {
      for (const { kind, mediaType } of documents) {
        const file = writeOfficeAttachmentFixture(directory, kind, updated)
        names.add(basename(file))
        await exerciseAttachmentDelivery(native, 'binary', basename(file), {
          fixturePath: file,
          mediaType,
          attachmentOnly: updated,
        })
      }
    }
    await native.page.reload()
    for (const name of names)
      await expect(userBubbles(native.page).filter({ hasText: name })).toHaveCount(2)
  })
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/511.
