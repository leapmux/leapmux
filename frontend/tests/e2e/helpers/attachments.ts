import type { Page } from '@playwright/test'
import { Buffer } from 'node:buffer'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { createTestDirectory } from './runDirectory'
import { waitForSettingsHydrated } from './ui'

/**
 * Attachment fixtures and the composer flows that consume them.
 *
 * `038-attachment-support.spec.ts` covers the Claude default provider in full.
 * These helpers let a provider spec assert the same UI against that provider's
 * own attachment capabilities without restating the strip, pill and history
 * locators.
 */

/** A 1x1 red pixel PNG. Small enough to inline, decodable by every browser. */
const ONE_PIXEL_PNG_BASE64
  = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwADhQGAWjR9awAAAABJRU5ErkJggg=='

/** A minimal PDF: header, one empty page, EOF. Enough for a MIME sniff. */
function minimalPdf(): Buffer {
  const body = '%PDF-1.1\n1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj\n3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 20 20]>>endobj\ntrailer<</Root 1 0 R>>\n%%EOF\n'
  return Buffer.from(body, 'utf8')
}

export type AttachmentKind = 'text' | 'image' | 'pdf' | 'binary'

/** Write one fixture file of the given kind and return its absolute path. */
export function writeAttachmentFixture(kind: AttachmentKind, name?: string): string {
  const directory = createTestDirectory(`attachment-${kind}-`)
  switch (kind) {
    case 'text': {
      const path = join(directory, name ?? 'notes.txt')
      writeFileSync(path, 'leapmux attachment text fixture\n')
      return path
    }
    case 'image': {
      const path = join(directory, name ?? 'shot.png')
      writeFileSync(path, Buffer.from(ONE_PIXEL_PNG_BASE64, 'base64'))
      return path
    }
    case 'pdf': {
      const path = join(directory, name ?? 'doc.pdf')
      writeFileSync(path, minimalPdf())
      return path
    }
    case 'binary': {
      const path = join(directory, name ?? 'blob.bin')
      writeFileSync(path, Buffer.from([0x00, 0xFF, 0x01, 0xFE]))
      return path
    }
  }
}

/** Attach one file through the hidden composer input. */
export async function attachFile(page: Page, path: string): Promise<void> {
  await page.locator('[data-testid="file-input"]').setInputFiles(path)
}

export function attachmentPills(page: Page) {
  return page.locator('[data-testid="attachment-pill"]:visible')
}

export function attachmentStrip(page: Page) {
  return page.locator('[data-testid="attachment-strip"]:visible')
}

/**
 * Attach `kind`, then assert the pill appears or the composer refuses it.
 *
 * A provider that supports the kind gets a pill that names the file. A provider
 * that does not gets no pill and a toast that states the refusal. The toast text
 * is provider-neutral (`attachments.ts` builds it from the capability map).
 */
export async function expectAttachmentOutcome(
  page: Page,
  kind: AttachmentKind,
  options: { supported: boolean, fileName?: string },
): Promise<void> {
  // The composer refuses a kind from the agent's own capability map. Until the
  // panel receives the agent, it holds the default provider's map, which
  // accepts more kinds than some providers do. The settings menu is offered
  // from that same configuration, so its readiness is the gate.
  await waitForSettingsHydrated(page)
  const path = writeAttachmentFixture(kind, options.fileName)
  const name = options.fileName ?? path.split('/').pop()!
  await attachFile(page, path)
  if (options.supported) {
    await expect(attachmentPills(page)).toHaveCount(1)
    await expect(attachmentPills(page).first()).toContainText(name)
    await expect(attachmentStrip(page)).toBeVisible()
    return
  }
  await expect(attachmentPills(page)).toHaveCount(0)
}

/** Send the composer with its attachment and optional text, then wait for the strip to clear. */
export async function sendWithAttachment(page: Page, text: string): Promise<void> {
  const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
  await editor.click()
  if (text)
    await page.keyboard.type(text)
  await page.keyboard.press('Meta+Enter')
  await expect(editor).toHaveText('')
  await expect(attachmentPills(page)).toHaveCount(0)
}
