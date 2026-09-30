import type { Page } from '@playwright/test'
import { Buffer } from 'node:buffer'
import { writeFileSync } from 'node:fs'
import { basename, join } from 'node:path'
import { crc32, deflateSync } from 'node:zlib'
import { expect } from '@playwright/test'
import { createTestDirectory } from './runDirectory'
import { waitForSettingsHydrated } from './ui'

/**
 * Attachment fixtures and the composer flows that consume them.
 *
 * `038-attachment-support.spec.ts` covers the Claude composer's attachment flow.
 * These helpers let a provider spec assert the same UI against that provider's
 * own attachment capabilities without restating the strip, pill and history
 * locators.
 */

const IMAGE_SIZE = 16
const PDF_PAGE_MARKER = 'LEAPMUX_PDF_PAGE_49'

function pngChunk(type: string, data: Buffer): Buffer {
  const kind = Buffer.from(type, 'ascii')
  const chunk = Buffer.alloc(12 + data.length)
  chunk.writeUInt32BE(data.length, 0)
  kind.copy(chunk, 4)
  data.copy(chunk, 8)
  chunk.writeUInt32BE(crc32(Buffer.concat([kind, data])), 8 + data.length)
  return chunk
}

/** Four large colors survive a format change and expose header-only copies. */
function quadrantPng(): Buffer {
  const header = Buffer.alloc(13)
  header.writeUInt32BE(IMAGE_SIZE, 0)
  header.writeUInt32BE(IMAGE_SIZE, 4)
  header[8] = 8 // channel depth
  header[9] = 2 // RGB

  const rowLength = 1 + IMAGE_SIZE * 3
  const pixels = Buffer.alloc(IMAGE_SIZE * rowLength)
  const colors = [
    [[255, 0, 0], [0, 255, 0]],
    [[0, 0, 255], [255, 255, 0]],
  ]
  for (let y = 0; y < IMAGE_SIZE; y++) {
    for (let x = 0; x < IMAGE_SIZE; x++) {
      const color = colors[y < IMAGE_SIZE / 2 ? 0 : 1]?.[x < IMAGE_SIZE / 2 ? 0 : 1]
      if (!color)
        throw new Error('the attachment image has no quadrant color')
      const offset = y * rowLength + 1 + x * 3
      pixels[offset] = color[0]!
      pixels[offset + 1] = color[1]!
      pixels[offset + 2] = color[2]!
    }
  }

  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A]),
    pngChunk('IHDR', header),
    pngChunk('IDAT', deflateSync(pixels)),
    pngChunk('IEND', Buffer.alloc(0)),
  ])
}

/** Write one PDF page with text and four color blocks. */
function markedPdf(): Buffer {
  const drawing = [
    '1 0 0 rg 0 100 100 100 re f',
    '0 1 0 rg 100 100 100 100 re f',
    '0 0 1 rg 0 0 100 100 re f',
    '1 1 0 rg 100 0 100 100 re f',
    '1 1 1 rg 0 85 200 30 re f',
    `0 0 0 rg BT /F1 10 Tf 8 97 Td (${PDF_PAGE_MARKER}) Tj ET`,
  ].join('\n').concat('\n')
  const objects = [
    '<< /Type /Catalog /Pages 2 0 R >>',
    '<< /Type /Pages /Kids [3 0 R] /Count 1 >>',
    '<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>',
    `<< /Length ${Buffer.byteLength(drawing, 'ascii')} >>\nstream\n${drawing}endstream`,
    '<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>',
  ]
  let body = '%PDF-1.4\n'
  const offsets: number[] = []
  for (const [index, object] of objects.entries()) {
    offsets.push(Buffer.byteLength(body, 'ascii'))
    body += `${index + 1} 0 obj\n${object}\nendobj\n`
  }
  const crossReference = Buffer.byteLength(body, 'ascii')
  body += `xref\n0 ${objects.length + 1}\n0000000000 65535 f \n`
  for (const offset of offsets)
    body += `${String(offset).padStart(10, '0')} 00000 n \n`
  body += `trailer\n<< /Size ${objects.length + 1} /Root 1 0 R >>\nstartxref\n${crossReference}\n%%EOF\n`
  return Buffer.from(body, 'ascii')
}

export type AttachmentKind = 'text' | 'image' | 'pdf' | 'binary'

export interface AttachmentOutcomeOptions {
  supported: boolean
  fileName?: string
  readyGroup?: string
  fixturePath?: string
}

/** Write one fixture file of the given kind and return its absolute path. */
export function writeAttachmentFixture(kind: AttachmentKind, name?: string): string {
  const directory = createTestDirectory(`attachment-${kind}-`)
  switch (kind) {
    case 'text': {
      const path = join(directory, name ?? 'notes.txt')
      writeFileSync(path, 'leapmux attachment text fixture')
      return path
    }
    case 'image': {
      const path = join(directory, name ?? 'shot.png')
      writeFileSync(path, quadrantPng())
      return path
    }
    case 'pdf': {
      const path = join(directory, name ?? 'doc.pdf')
      writeFileSync(path, markedPdf())
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

/** Use a caller's fixture when the provider needs a different valid file. */
export function selectAttachmentFixture(kind: AttachmentKind, options: AttachmentOutcomeOptions): string {
  if (options.fixturePath !== undefined) {
    if (options.fileName !== undefined && basename(options.fixturePath) !== options.fileName)
      throw new Error('The attachment fixture path and file name must agree')
    return options.fixturePath
  }
  return writeAttachmentFixture(kind, options.fileName)
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
  options: AttachmentOutcomeOptions,
): Promise<string> {
  // The composer refuses a kind from the agent's own capability map. Until the
  // panel receives the agent, it holds the default provider's map, which
  // accepts more kinds than some providers do. The settings menu is offered
  // from that same configuration, so its readiness is the gate.
  await waitForSettingsHydrated(page, options.readyGroup)
  const path = selectAttachmentFixture(kind, options)
  const name = basename(path)
  await attachFile(page, path)
  if (options.supported) {
    await expect(attachmentPills(page)).toHaveCount(1)
    await expect(attachmentPills(page).first()).toContainText(name)
    await expect(attachmentStrip(page)).toBeVisible()
    return path
  }
  await expect(attachmentPills(page)).toHaveCount(0)
  await expect(page.locator('output .toast-message').filter({ hasText: new RegExp(kind, 'i') })).toBeVisible()
  return path
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
