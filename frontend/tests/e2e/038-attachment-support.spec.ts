import type { Page } from '@playwright/test'
import { readFileSync } from 'node:fs'
import { expect } from '@playwright/test'
import { test } from './fixtures'
import { attachFile, attachmentPills, attachmentStrip, writeAttachmentFixture } from './helpers/attachments'
import { nativeModelConversationTurns, selectedAgentTab, selectedAgentTabId } from './helpers/nativeScenario'
import { sendScriptedTurn } from './helpers/scriptedTurn'
import { expectToastRecorded } from './helpers/toast'
import { composerEditor, expectClipsToOneLine, focusComposer, openAgentViaUI, tabById, userBubbles, waitForSettingsHydrated } from './helpers/ui'

/**
 * How the clipboard of a paste lists its file:
 *
 * - `listed`: in `files` and in `items`, as Chromium lists it.
 * - `empty`: in `items` only, as WebKitGTK lists a pasted image. The paste replaces Chromium's `files` getter to
 *   reproduce that shape.
 */
type PastedFiles = 'listed' | 'empty'

/**
 * Paste a PNG file into the visible composer through a synthetic `paste` event, as `pasteText` in
 * `helpers/composer.ts` pastes text. The event reaches the paste listener of the composer as a real paste does.
 */
async function pastePng(page: Page, options: { name: string, files: PastedFiles }): Promise<void> {
  await composerEditor(page).evaluate((editor, { name, files }) => {
    const png = new File([new Uint8Array([0x89, 0x50, 0x4E, 0x47])], name, { type: 'image/png' })
    const clipboardData = new DataTransfer()
    clipboardData.items.add(png)
    if (files === 'empty')
      Object.defineProperty(clipboardData, 'files', { value: new DataTransfer().files })
    editor.dispatchEvent(new ClipboardEvent('paste', { clipboardData, bubbles: true, cancelable: true }))
  }, options)
}

/** Hold one selected file until the test releases its actual browser read. */
async function holdAttachmentRead(page: Page, filename: string) {
  return page.evaluateHandle((filename) => {
    const original = FileReader.prototype.readAsArrayBuffer
    const pending: { reader: FileReader, file: Blob }[] = []
    FileReader.prototype.readAsArrayBuffer = function (file) {
      if (file instanceof File && file.name === filename)
        pending.push({ reader: this, file })
      else
        original.call(this, file)
    }
    return async () => {
      FileReader.prototype.readAsArrayBuffer = original
      if (pending.length !== 1)
        throw new Error(`Expected one held attachment read, received ${pending.length}.`)
      await Promise.all(pending.map(({ reader, file }) => new Promise<void>((resolve) => {
        reader.addEventListener('loadend', () => queueMicrotask(resolve), { once: true })
        original.call(reader, file)
      })))
    }
  }, filename)
}

test.describe('attachment support', () => {
  test('keeps a pending file read out of another tab and preserves both existing drafts', async ({ page, authenticatedWorkspace }) => {
    await expect(composerEditor(page)).toBeVisible()
    await attachFile(page, writeAttachmentFixture('text', 'original.txt'))
    await expect(attachmentPills(page)).toContainText('original.txt')
    const originalId = authenticatedWorkspace.agentId
    const release = await holdAttachmentRead(page, 'pending.txt')
    await attachFile(page, writeAttachmentFixture('text', 'pending.txt'))
    await openAgentViaUI(page)
    await expect(selectedAgentTab(page)).not.toHaveAttribute('data-tab-id', originalId)
    await waitForSettingsHydrated(page)
    await attachFile(page, writeAttachmentFixture('text', 'destination.txt'))
    await expect(attachmentPills(page)).toContainText('destination.txt')
    await release.evaluate(release => release())
    await expect(attachmentPills(page)).toHaveCount(1)
    await expect(attachmentPills(page)).toContainText('destination.txt')
    await tabById(page, originalId).click()
    await waitForSettingsHydrated(page)
    await expect(attachmentPills(page)).toHaveCount(1)
    await expect(attachmentPills(page)).toContainText('original.txt')
  })

  test('keeps removals and completed additions while another file read is pending', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    await expect(composerEditor(page)).toBeVisible()
    await attachFile(page, writeAttachmentFixture('text', 'removed.txt'))
    await expect(attachmentPills(page)).toContainText('removed.txt')
    const release = await holdAttachmentRead(page, 'pending.txt')
    await attachFile(page, writeAttachmentFixture('text', 'pending.txt'))
    await attachmentPills(page).getByTestId('attachment-remove').click()
    await attachFile(page, writeAttachmentFixture('text', 'other.txt'))
    await expect(attachmentPills(page)).toContainText('other.txt')
    await release.evaluate(release => release())
    await expect(attachmentPills(page)).toHaveCount(2)
    await expect(attachmentPills(page).filter({ hasText: 'pending.txt' })).toHaveCount(1)
    await expect(attachmentPills(page).filter({ hasText: 'other.txt' })).toHaveCount(1)
    await expect(attachmentPills(page).filter({ hasText: 'removed.txt' })).toHaveCount(0)
  })

  test('reports an unreadable selection and keeps the readable files', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    await expect(composerEditor(page)).toBeVisible()
    await page.evaluate(() => {
      const original = FileReader.prototype.readAsArrayBuffer
      FileReader.prototype.readAsArrayBuffer = function (file) {
        if (file instanceof File && file.name === 'broken.txt') {
          queueMicrotask(() => {
            Object.defineProperty(this, 'error', { value: new DOMException('The file is unavailable.', 'NotReadableError') })
            this.dispatchEvent(new ProgressEvent('error'))
          })
        }
        else {
          original.call(this, file)
        }
      }
    })
    await page.getByTestId('file-input').setInputFiles([
      writeAttachmentFixture('text', 'first.txt'),
      writeAttachmentFixture('text', 'broken.txt'),
      writeAttachmentFixture('text', 'last.txt'),
    ])
    await expect(attachmentPills(page)).toHaveCount(2)
    await expect(attachmentPills(page).filter({ hasText: 'first.txt' })).toHaveCount(1)
    await expect(attachmentPills(page).filter({ hasText: 'last.txt' })).toHaveCount(1)
    await expectToastRecorded(page, /Could not read attachment "broken\.txt"/)
  })

  test('keeps readable files when a dropped directory has entry and enumeration failures', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    await expect(composerEditor(page)).toBeVisible()
    await composerEditor(page).evaluate((editor) => {
      const transfer = new DataTransfer()
      const item = transfer.items.add(new File([], 'project'))
      if (!item)
        throw new Error('The browser refused the directory drop fixture.')
      const entries = ['first.txt', 'broken.txt', 'last.txt'].map(name => ({
        isFile: true,
        isDirectory: false,
        name,
        fullPath: `/project/${name}`,
        file: (success: (file: File) => void, failure: (error: DOMException) => void) => {
          if (name === 'broken.txt')
            failure(new DOMException('Unavailable', 'NotReadableError'))
          else
            success(new File(['readable file'], name, { type: 'text/plain' }))
        },
      }))
      Object.defineProperty(item, 'webkitGetAsEntry', { value: () => ({
        isFile: false,
        isDirectory: true,
        name: 'project',
        fullPath: '/project',
        createReader: () => {
          let first = true
          return { readEntries: (success: (entries: unknown[]) => void, failure: (error: DOMException) => void) => {
            if (first) {
              first = false
              success(entries)
            }
            else {
              failure(new DOMException('Unavailable', 'NotReadableError'))
            }
          } }
        },
      }) })
      // Chromium returns a new item wrapper on each lookup. Keep the configured
      // wrapper in this event so the directory entry remains observable.
      Object.defineProperty(transfer, 'items', { value: [item] })
      for (const type of ['dragenter', 'dragover', 'drop'])
        editor.dispatchEvent(new DragEvent(type, { dataTransfer: transfer, bubbles: true, cancelable: true }))
    })
    await expect(attachmentPills(page)).toHaveCount(2)
    await expect(attachmentPills(page).filter({ hasText: 'project/first.txt' })).toHaveCount(1)
    await expect(attachmentPills(page).filter({ hasText: 'project/last.txt' })).toHaveCount(1)
    await expectToastRecorded(page, /Could not read the dropped files: .*project\/broken\.txt/)
  })

  test('attach item opens file dialog and attachment appears in strip', async ({ page, authenticatedWorkspace }) => {
    await expect(composerEditor(page)).toBeVisible()

    // The plus menu contains the attachment control.
    await page.locator('[data-testid="composer-plus-trigger"]').click()
    const attach = page.locator('[data-testid="composer-attach-file"]')
    await expect(attach).toBeVisible()
    await expect(attach).toBeEnabled()
    await page.keyboard.press('Escape')

    // Upload a file through the hidden input.
    await attachFile(page, writeAttachmentFixture('image', 'screenshot.png'))

    // Require the attachment strip and one pill.
    await expect(attachmentStrip(page)).toBeVisible()
    const pill = attachmentPills(page)
    await expect(pill).toHaveCount(1)
    await expect(pill).toContainText('screenshot.png')

    // The filename must fit one line inside the pill's 200px maximum width.
    // An ellipsis alone cannot shrink a flex item below its text width. The item also needs min-width: 0.
    // Only a browser can resolve the combined rules.
    // Select span[class] to identify the label. Tooltip creates an earlier display: contents span with the same text.
    // A plain span lookup selects that wrapper and cannot prove the label's clipping.
    await expectClipsToOneLine(pill.locator('span[class]').filter({ hasText: 'screenshot.png' }))
  })

  test('remove attachment via X button', async ({ page, authenticatedWorkspace }) => {
    await expect(composerEditor(page)).toBeVisible()

    // Upload a file.
    await attachFile(page, writeAttachmentFixture('image'))

    await expect(attachmentPills(page)).toHaveCount(1)

    // Click the remove button.
    await attachmentPills(page).locator('[data-testid="attachment-remove"]').click()
    await expect(attachmentPills(page)).toHaveCount(0)
    // Require the hidden empty strip.
    await expect(attachmentStrip(page)).not.toBeVisible()
  })

  test('attachments survive tab switch', async ({ page, authenticatedWorkspace }) => {
    await expect(composerEditor(page)).toBeVisible()

    // Upload a file.
    await attachFile(page, writeAttachmentFixture('image', 'persist.png'))
    await expect(attachmentPills(page)).toHaveCount(1)
    const originalId = await selectedAgentTabId(page)
    expect(originalId).toBe(authenticatedWorkspace.agentId)

    // Open a new agent tab. The attachments belong to the composer of a tab. The selected
    // tab proves the switch, and the settings wait proves that the composer of the new
    // agent is on screen.
    await openAgentViaUI(page)
    const active = selectedAgentTab(page)
    await expect(active).not.toHaveAttribute('data-tab-id', originalId)
    await waitForSettingsHydrated(page)

    // Require no attachment on the new tab.
    await expect(attachmentPills(page)).toHaveCount(0)

    // Select the first tab.
    await tabById(page, originalId).click()
    await expect(active).toHaveAttribute('data-tab-id', originalId)
    await waitForSettingsHydrated(page)

    // Require the retained attachment.
    await expect(attachmentPills(page)).toHaveCount(1)
    await expect(attachmentPills(page)).toContainText('persist.png')
  })

  test('attachments cleared after send', async ({ page, authenticatedWorkspace, modelScript }) => {
    await expect(composerEditor(page)).toBeVisible()

    // Upload a file.
    await attachFile(page, writeAttachmentFixture('image'))
    await expect(attachmentPills(page)).toHaveCount(1)

    // Check the current user turn before checking the attachment reset.
    const step = await sendScriptedTurn(page, modelScript, { prompt: 'look at this', answer: 'The attachment turn completed.' })
    const request = await modelScript.requestAt(step)
    const userTurn = nativeModelConversationTurns(request).filter(turn => turn.role === 'user').at(-1)
    expect(userTurn?.text).toContain('look at this')
    await expect(userBubbles(page).filter({ hasText: 'look at this' }).first()).toBeVisible()

    // Require removal of the sent attachment.
    await expect(attachmentPills(page)).toHaveCount(0)
  })

  test('paste image adds attachment', async ({ page, authenticatedWorkspace }) => {
    await focusComposer(page)

    await pastePng(page, { name: 'pasted.png', files: 'listed' })

    // Require the attachment pill.
    await expect(attachmentPills(page)).toHaveCount(1)
  })

  test('paste image adds attachment when clipboardData.files is empty (Linux/WebKitGTK shape)', async ({ page, authenticatedWorkspace }) => {
    await focusComposer(page)

    // WebKitGTK exposes pasted images through items but leaves files empty.
    await pastePng(page, { name: '', files: 'empty' })

    // Require the attachment pill.
    await expect(attachmentPills(page)).toHaveCount(1)
  })

  test('drag and drop adds attachment', async ({ page, authenticatedWorkspace }) => {
    const editor = composerEditor(page)
    await expect(editor).toBeVisible()

    // Deliver valid image bytes through the editor's actual drop event route.
    const bytes = Array.from(readFileSync(writeAttachmentFixture('image', 'dropped.png')))
    await editor.evaluate((element, bytes) => {
      const transfer = new DataTransfer()
      transfer.items.add(new File([new Uint8Array(bytes)], 'dropped.png', { type: 'image/png' }))
      for (const type of ['dragenter', 'dragover', 'drop'])
        element.dispatchEvent(new DragEvent(type, { dataTransfer: transfer, bubbles: true, cancelable: true }))
    }, bytes)

    await expect(attachmentPills(page)).toHaveCount(1)
    await expect(attachmentPills(page)).toContainText('dropped.png')
  })
})
