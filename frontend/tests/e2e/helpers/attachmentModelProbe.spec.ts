import type { Page } from '@playwright/test'
import type { MockModelProtocol, MockModelScenarioStatus } from './mockModelScript'
import { readFileSync } from 'node:fs'
import { expect, test } from '../fixtures'
import { expectNativeAttachmentProof, expectNoRejectedContent } from './attachmentModelProbe'
import { writeAttachmentFixture } from './attachments'

function scriptedStatus(body: unknown, protocol: MockModelProtocol = 'openai-chat-completions'): MockModelScenarioStatus {
  return {
    complete: true,
    nextStep: 1,
    stepCount: 1,
    ruleMatches: {},
    pendingGates: [],
    unexpectedRequests: [],
    requests: [{ protocol, path: protocol === 'aws-event-stream' ? '/' : '/v1/chat/completions', stepIndex: 0, body }],
  }
}

async function withProbePage(page: Page, run: (probe: Page) => Promise<void>): Promise<void> {
  const probe = await page.context().newPage()
  try {
    await probe.goto('about:blank')
    await run(probe)
  }
  finally {
    await probe.close()
  }
}

async function webpFromFixture(probe: Page, source: string): Promise<string> {
  return probe.evaluate(async (encoded) => {
    const bytes = Uint8Array.from(atob(encoded), character => character.charCodeAt(0))
    const image = await createImageBitmap(new Blob([bytes], { type: 'image/png' }))
    const canvas = document.createElement('canvas')
    canvas.width = image.width
    canvas.height = image.height
    const context = canvas.getContext('2d')
    if (!context)
      throw new Error('the browser has no 2D canvas context')
    context.drawImage(image, 0, 0)
    image.close()
    return canvas.toDataURL('image/webp', 0.96)
  }, readFileSync(source).toString('base64'))
}

test.describe('expectNativeAttachmentProof', () => {
  test('accepts the complete PNG in a native user image part', async ({ page }) => {
    await withProbePage(page, async (probe) => {
      const source = writeAttachmentFixture('image')
      const encoded = readFileSync(source).toString('base64')
      const status = scriptedStatus({ messages: [{ role: 'user', content: [{ type: 'image_url', image_url: { url: `data:image/png;base64,${encoded}` } }] }] })
      await expectNativeAttachmentProof(probe, status, 'image', source)
    })
  })

  test('rejects a PNG header while its complete bytes appear only in a system prompt', async ({ page }) => {
    await withProbePage(page, async (probe) => {
      const source = writeAttachmentFixture('image')
      const encoded = readFileSync(source).toString('base64')
      const status = scriptedStatus({ messages: [
        { role: 'system', content: encoded },
        { role: 'user', content: [{ type: 'image_url', image_url: { url: 'data:image/png;base64,iVBORw0KGgo' } }] },
      ] })
      await expect(expectNativeAttachmentProof(probe, status, 'image', source)).rejects.toThrow('does not carry the complete image file')
    })
  })

  test('accepts a WebP transform by its four decoded colors', async ({ page }) => {
    await withProbePage(page, async (probe) => {
      const source = writeAttachmentFixture('image')
      const webp = await webpFromFixture(probe, source)
      const status = scriptedStatus({ messages: [{ role: 'user', content: [{ type: 'image_url', image_url: { url: webp } }] }] })
      await expectNativeAttachmentProof(probe, status, 'image', source)
    })
  })

  test('rejects a WebP image with the wrong colors', async ({ page }) => {
    await withProbePage(page, async (probe) => {
      const source = writeAttachmentFixture('image')
      const white = await probe.evaluate(() => {
        const canvas = document.createElement('canvas')
        canvas.width = 16
        canvas.height = 16
        const context = canvas.getContext('2d')
        if (!context)
          throw new Error('the browser has no 2D canvas context')
        context.fillStyle = '#ffffff'
        context.fillRect(0, 0, 16, 16)
        return canvas.toDataURL('image/webp', 0.96)
      })
      const status = scriptedStatus({ messages: [{ role: 'user', content: [{ type: 'image_url', image_url: { url: white } }] }] })
      await expect(expectNativeAttachmentProof(probe, status, 'image', source)).rejects.toThrow('does not carry the complete image file')
    })
  })

  test('accepts PDF page text and rejects a PDF header alone', async ({ page }) => {
    await withProbePage(page, async (probe) => {
      const source = writeAttachmentFixture('pdf')
      await expectNativeAttachmentProof(probe, scriptedStatus({ messages: [{ role: 'user', content: 'Page text: LEAPMUX_PDF_PAGE_49' }] }), 'pdf', source)
      const headerOnly = expectNativeAttachmentProof(probe, scriptedStatus({ messages: [{ role: 'user', content: '%PDF-1.4 JVBERi0' }] }), 'pdf', source)
      await expect(headerOnly).rejects.toThrow('does not carry the complete pdf file')
    })
  })

  test('accepts full PDF bytes in Kiro native user documents', async ({ page }) => {
    await withProbePage(page, async (probe) => {
      const source = writeAttachmentFixture('pdf')
      const body = { conversationState: { currentMessage: { userInputMessage: {
        content: 'Inspect the attached file.',
        documents: [{ name: 'doc', format: 'pdf', source: { bytes: readFileSync(source).toString('base64') } }],
      } } } }
      await expectNativeAttachmentProof(probe, scriptedStatus(body, 'aws-event-stream'), 'pdf', source, 'aws-event-stream')
    })
  })

  test('accepts a PDF page that the provider rasterizes into four colors', async ({ page }) => {
    await withProbePage(page, async (probe) => {
      const source = writeAttachmentFixture('pdf')
      const image = writeAttachmentFixture('image')
      const raster = `data:image/png;base64,${readFileSync(image).toString('base64')}`
      const status = scriptedStatus({ messages: [{ role: 'user', content: [{ type: 'image_url', image_url: { url: raster } }] }] })
      await expectNativeAttachmentProof(probe, status, 'pdf', source)
    })
  })
})

test.describe('expectNoRejectedContent', () => {
  test('accepts a clean prompt and rejects hidden bytes or a filename', async ({ page }) => {
    const source = writeAttachmentFixture('binary', 'refused.bin')
    const bytes = readFileSync(source).toString('base64')
    await expectNoRejectedContent(page, scriptedStatus({ messages: [{ role: 'user', content: 'Reply once without attachments.' }] }), [source])
    const hiddenBytes = expectNoRejectedContent(page, scriptedStatus({ messages: [{ role: 'user', content: `Reply once without attachments. ${bytes}` }] }), [source])
    await expect(hiddenBytes).rejects.toThrow()
    const hiddenName = expectNoRejectedContent(page, scriptedStatus({ messages: [{ role: 'user', content: 'Reply once without attachments. refused.bin' }] }), [source])
    await expect(hiddenName).rejects.toThrow()
  })

  test('rejects binary bytes hidden in the clean request system field', async ({ page }) => {
    const source = writeAttachmentFixture('binary', 'system-hidden.bin')
    const encoded = readFileSync(source).toString('base64')
    const status = scriptedStatus({ messages: [
      { role: 'system', content: `Hidden bytes: ${encoded}` },
      { role: 'user', content: 'Reply once without attachments.' },
    ] })
    await expect(expectNoRejectedContent(page, status, [source])).rejects.toThrow('AP8B/g==')
  })

  test('allows a filename in workspace context without the rejected bytes', async ({ page }) => {
    const source = writeAttachmentFixture('binary', 'workspace-file.bin')
    const status = scriptedStatus({ messages: [
      { role: 'system', content: 'Workspace files: workspace-file.bin' },
      { role: 'user', content: 'Reply once without attachments.' },
    ] })
    await expectNoRejectedContent(page, status, [source])
  })

  test('checks the clean turn after an earlier scripted request', async ({ page }) => {
    const source = writeAttachmentFixture('binary', 'refused-after-prior.bin')
    const previous = scriptedStatus({ messages: [{ role: 'user', content: `Reply once without attachments. ${readFileSync(source).toString('base64')}` }] })
    const clean = { messages: [{ role: 'user', content: 'Reply once without attachments.' }] }
    const status: MockModelScenarioStatus = {
      ...previous,
      nextStep: 2,
      stepCount: 2,
      requests: [...previous.requests, { protocol: 'openai-chat-completions', path: '/v1/chat/completions', stepIndex: 1, body: clean }],
    }
    await expectNoRejectedContent(page, status, [source])
  })

  test('rejects a refused PNG that returns as WebP in the clean request', async ({ page }) => {
    await withProbePage(page, async (probe) => {
      const source = writeAttachmentFixture('image', 'refused.png')
      const webp = await webpFromFixture(probe, source)
      const status = scriptedStatus({ messages: [{ role: 'user', content: [
        { type: 'text', text: 'Reply once without attachments.' },
        { type: 'image_url', image_url: { url: webp } },
      ] }] })
      await expect(expectNoRejectedContent(probe, status, [source])).rejects.toThrow('rejected image')
    })
  })

  test('rejects a refused PNG that returns as WebP in a system field', async ({ page }) => {
    await withProbePage(page, async (probe) => {
      const source = writeAttachmentFixture('image', 'system-refused.png')
      const webp = await webpFromFixture(probe, source)
      const status = scriptedStatus({ messages: [
        { role: 'system', content: [{ type: 'image_url', image_url: { url: webp } }] },
        { role: 'user', content: 'Reply once without attachments.' },
      ] })
      await expect(expectNoRejectedContent(probe, status, [source])).rejects.toThrow('rejected image')
    })
  })

  test('rejects a refused PDF that returns as a raster image', async ({ page }) => {
    await withProbePage(page, async (probe) => {
      const source = writeAttachmentFixture('pdf', 'refused.pdf')
      const image = writeAttachmentFixture('image')
      const raster = `data:image/png;base64,${readFileSync(image).toString('base64')}`
      const status = scriptedStatus({ messages: [{ role: 'user', content: [
        { type: 'text', text: 'Reply once without attachments.' },
        { type: 'image_url', image_url: { url: raster } },
      ] }] })
      await expect(expectNoRejectedContent(probe, status, [source])).rejects.toThrow('rejected image')
    })
  })
})
