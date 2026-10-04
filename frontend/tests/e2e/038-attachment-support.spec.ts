import { readFileSync } from 'node:fs'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { expect, test } from './fixtures'
import { writeAttachmentFixture } from './helpers/attachments'
import { sendNativeAnswer } from './helpers/nativeConversation'
import { currentNativeAgent } from './helpers/nativeScenario'
import { expectClipsToOneLine, tabById, userBubbles, waitForSettingsHydrated } from './helpers/ui'

test.describe('Attachment Support', () => {
  test('attach item opens file dialog and attachment appears in strip', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // The plus menu contains the attachment control.
    await page.locator('[data-testid="composer-plus-trigger"]').click()
    const attach = page.locator('[data-testid="composer-attach-file"]')
    await expect(attach).toBeVisible()
    await expect(attach).toBeEnabled()
    await page.keyboard.press('Escape')

    // Upload a file through the hidden input.
    const fileInput = page.locator('[data-testid="file-input"]')
    await fileInput.setInputFiles(writeAttachmentFixture('image', 'screenshot.png'))

    // Require the attachment strip and one pill.
    const strip = page.locator('[data-testid="attachment-strip"]')
    await expect(strip).toBeVisible()
    const pill = page.locator('[data-testid="attachment-pill"]')
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
    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Upload a file.
    const fileInput = page.locator('[data-testid="file-input"]')
    await fileInput.setInputFiles(writeAttachmentFixture('image'))

    await expect(page.locator('[data-testid="attachment-pill"]')).toHaveCount(1)

    // Click the remove button.
    await page.locator('[data-testid="attachment-remove"]').click()
    await expect(page.locator('[data-testid="attachment-pill"]')).toHaveCount(0)
    // Require the hidden empty strip.
    await expect(page.locator('[data-testid="attachment-strip"]')).not.toBeVisible()
  })

  test('attachments survive tab switch', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Upload a file.
    const fileInput = page.locator('[data-testid="file-input"]')
    await fileInput.setInputFiles(writeAttachmentFixture('image', 'persist.png'))
    await expect(page.locator('[data-testid="attachment-pill"]')).toHaveCount(1)
    const context = { page, leapmuxServer }
    const original = await currentNativeAgent(context)

    // Open a new agent tab.
    await page.locator('[data-testid^="new-agent-button"]:visible').first().click()
    const active = page.locator('[data-testid="tab"][data-tab-type="agent"][aria-selected="true"]:visible').first()
    await expect(active).toBeVisible()
    await expect(active).not.toHaveAttribute('data-tab-id', original.id)
    expect((await currentNativeAgent(context)).id).not.toBe(original.id)
    await waitForSettingsHydrated(page)

    // Require no attachment on the new tab.
    await expect(page.locator('[data-testid="attachment-pill"]')).toHaveCount(0)

    // Select the first tab.
    await tabById(page, original.id).click()
    expect((await currentNativeAgent(context)).id).toBe(original.id)
    await waitForSettingsHydrated(page)

    // Require the retained attachment.
    await expect(page.locator('[data-testid="attachment-pill"]')).toHaveCount(1)
    await expect(page.locator('[data-testid="attachment-pill"]')).toContainText('persist.png')
  })

  test('attachments cleared after send', async ({ page, authenticatedWorkspace, modelScript }) => {
    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Upload a file.
    const fileInput = page.locator('[data-testid="file-input"]')
    await fileInput.setInputFiles(writeAttachmentFixture('image'))
    await expect(page.locator('[data-testid="attachment-pill"]')).toHaveCount(1)

    // Require native prompt delivery and a completed answer before checking the strip reset.
    await sendNativeAnswer({ page, modelScript, provider: AgentProvider.CLAUDE_CODE }, 'look at this', 'The attached image reached the completed native turn.')
    await expect(userBubbles(page).filter({ hasText: 'look at this' }).first()).toBeVisible()

    // Require removal of the sent attachment.
    await expect(page.locator('[data-testid="attachment-pill"]')).toHaveCount(0)
  })

  test('paste image adds attachment', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Paste an image through a clipboard event.
    await page.evaluate(() => {
      const blob = new Blob([new Uint8Array([0x89, 0x50, 0x4E, 0x47])], { type: 'image/png' })
      const file = new File([blob], 'pasted.png', { type: 'image/png' })
      const dt = new DataTransfer()
      dt.items.add(file)
      const event = new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true })
      document.querySelector('[data-testid="composer-editor"]')!.dispatchEvent(event)
    })

    // Require the attachment pill.
    await expect(page.locator('[data-testid="attachment-pill"]')).toHaveCount(1)
  })

  test('paste image adds attachment when clipboardData.files is empty (Linux/WebKitGTK shape)', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // WebKitGTK exposes pasted images through items but leaves files empty.
    // Override Chromium's files getter to reproduce that clipboard shape.
    await page.evaluate(() => {
      const blob = new Blob([new Uint8Array([0x89, 0x50, 0x4E, 0x47])], { type: 'image/png' })
      const file = new File([blob], '', { type: 'image/png' })
      const clipboardData = new DataTransfer()
      clipboardData.items.add(file)
      Object.defineProperty(clipboardData, 'files', { value: new DataTransfer().files })
      const event = new ClipboardEvent('paste', { clipboardData, bubbles: true, cancelable: true })
      document.querySelector('[data-testid="composer-editor"]')!.dispatchEvent(event)
    })

    // Require the attachment pill.
    await expect(page.locator('[data-testid="attachment-pill"]')).toHaveCount(1)
  })

  test('drag and drop adds attachment', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Deliver valid image bytes through the editor's actual drop event route.
    const bytes = Array.from(readFileSync(writeAttachmentFixture('image', 'dropped.png')))
    await editor.evaluate((element, bytes) => {
      const transfer = new DataTransfer()
      transfer.items.add(new File([new Uint8Array(bytes)], 'dropped.png', { type: 'image/png' }))
      for (const type of ['dragenter', 'dragover', 'drop'])
        element.dispatchEvent(new DragEvent(type, { dataTransfer: transfer, bubbles: true, cancelable: true }))
    }, bytes)

    await expect(page.locator('[data-testid="attachment-pill"]')).toHaveCount(1)
    await expect(page.locator('[data-testid="attachment-pill"]')).toContainText('dropped.png')
  })
})
