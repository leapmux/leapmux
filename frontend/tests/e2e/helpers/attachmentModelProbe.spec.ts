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
  test('rejects a PDF marker without exact native PDF handoff', async ({ page }) => {
    await withProbePage(page, async (probe) => {
      const source = writeAttachmentFixture('pdf', 'marker-only.pdf')
      const status = scriptedStatus({ messages: [{ role: 'user', content: 'LEAPMUX_PDF_PAGE_49' }] })
      await expect(expectNativeAttachmentProof(probe, status, 'pdf', source)).rejects.toThrow()
    })
  })

  test('rejects an unrelated quadrant image without exact native PDF handoff', async ({ page }) => {
    await withProbePage(page, async (probe) => {
      const source = writeAttachmentFixture('pdf', 'image-only.pdf')
      const image = writeAttachmentFixture('image', 'unrelated-image.png')
      const raster = `data:image/png;base64,${readFileSync(image).toString('base64')}`
      const status = scriptedStatus({ messages: [{ role: 'user', content: [{ type: 'image_url', image_url: { url: raster } }] }] })
      await expect(expectNativeAttachmentProof(probe, status, 'pdf', source)).rejects.toThrow()
    })
  })

  test('rejects complete PDF bytes in plain text instead of a native PDF part', async ({ page }) => {
    await withProbePage(page, async (probe) => {
      const source = writeAttachmentFixture('pdf', 'plain-text-bytes.pdf')
      const status = scriptedStatus({ messages: [{ role: 'user', content: readFileSync(source).toString('base64') }] })
      await expect(expectNativeAttachmentProof(probe, status, 'pdf', source)).rejects.toThrow()
    })
  })

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
      await expectNativeAttachmentProof(probe, status, 'image', source, 'openai-chat-completions', { transcodedImageType: 'image/webp' })
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
      // The allowance admits a WebP, so only the decoded colors reject this image.
      const wrongColors = expectNativeAttachmentProof(probe, status, 'image', source, 'openai-chat-completions', { transcodedImageType: 'image/webp' })
      await expect(wrongColors).rejects.toThrow('does not carry the complete image file')
      await expect(wrongColors).rejects.toThrow('decodes to the quadrant pixels')
    })
  })

  test('rejects a WebP transform when the provider sends the PNG bytes unchanged', async ({ page }) => {
    await withProbePage(page, async (probe) => {
      const source = writeAttachmentFixture('image')
      const webp = await webpFromFixture(probe, source)
      const status = scriptedStatus({ messages: [{ role: 'user', content: [{ type: 'image_url', image_url: { url: webp } }] }] })
      await expect(expectNativeAttachmentProof(probe, status, 'image', source)).rejects.toThrow('does not carry the complete image file')
    })
  })

  test('rejects a WebP transform that declares image/png', async ({ page }) => {
    await withProbePage(page, async (probe) => {
      const source = writeAttachmentFixture('image')
      const webp = await webpFromFixture(probe, source)
      const mislabeled = webp.replace(/^data:image\/webp;/, 'data:image/png;')
      expect(mislabeled).not.toBe(webp)
      const status = scriptedStatus({ messages: [{ role: 'user', content: [{ type: 'image_url', image_url: { url: mislabeled } }] }] })
      await expect(expectNativeAttachmentProof(probe, status, 'image', source)).rejects.toThrow('does not carry the complete image file')
    })
  })

  test('rejects the complete PNG in non-canonical base64', async ({ page }) => {
    await withProbePage(page, async (probe) => {
      const source = writeAttachmentFixture('image')
      const encoded = readFileSync(source).toString('base64')
      const unpadded = encoded.replace(/=+$/, '')
      expect(unpadded).not.toBe(encoded)
      const status = scriptedStatus({ messages: [{ role: 'user', content: [{ type: 'image_url', image_url: { url: `data:image/png;base64,${unpadded}` } }] }] })
      await expect(expectNativeAttachmentProof(probe, status, 'image', source)).rejects.toThrow('does not carry the complete image file')
    })
  })

  test('rejects a quadrant-color image data URI in plain user text', async ({ page }) => {
    await withProbePage(page, async (probe) => {
      const source = writeAttachmentFixture('image')
      const webp = await webpFromFixture(probe, source)
      const status = scriptedStatus({ messages: [{ role: 'user', content: `Inspect this image: ${webp}` }] })
      await expect(expectNativeAttachmentProof(probe, status, 'image', source)).rejects.toThrow('does not carry the complete image file')
    })
  })

  test('rejects a quadrant-color image that only an earlier turn carries', async ({ page }) => {
    await withProbePage(page, async (probe) => {
      const source = writeAttachmentFixture('image')
      const webp = await webpFromFixture(probe, source)
      const status = scriptedStatus({ messages: [
        { role: 'user', content: [{ type: 'image_url', image_url: { url: webp } }] },
        { role: 'assistant', content: 'I read the image.' },
        { role: 'user', content: 'Inspect the attached file.' },
      ] })
      await expect(expectNativeAttachmentProof(probe, status, 'image', source)).rejects.toThrow('does not carry the complete image file')
    })
  })

  test('rejects a quadrant-color image inside an Anthropic tool result', async ({ page }) => {
    await withProbePage(page, async (probe) => {
      const source = writeAttachmentFixture('image')
      const webp = await webpFromFixture(probe, source)
      const data = webp.slice(webp.indexOf(',') + 1)
      const status = scriptedStatus({ messages: [
        { role: 'user', content: [{ type: 'text', text: 'Inspect the attached file.' }] },
        { role: 'assistant', content: [{ type: 'tool_use', id: 'toolu_1', name: 'Read', input: {} }] },
        { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'toolu_1', content: [{ type: 'image', source: { type: 'base64', media_type: 'image/webp', data } }] }] },
      ] }, 'anthropic-messages')
      await expect(expectNativeAttachmentProof(probe, status, 'image', source)).rejects.toThrow('does not carry the complete image file')
    })
  })

  test('rejects a quadrant-color image in a system row', async ({ page }) => {
    await withProbePage(page, async (probe) => {
      const source = writeAttachmentFixture('image')
      const webp = await webpFromFixture(probe, source)
      const status = scriptedStatus({ messages: [
        { role: 'user', content: 'Inspect the attached file.' },
        { role: 'system', content: [{ type: 'image_url', image_url: { url: webp } }] },
      ] })
      await expect(expectNativeAttachmentProof(probe, status, 'image', source)).rejects.toThrow('does not carry the complete image file')
    })
  })

  test('rejects PDF page text and a PDF header alone', async ({ page }) => {
    await withProbePage(page, async (probe) => {
      const source = writeAttachmentFixture('pdf')
      const pageText = expectNativeAttachmentProof(probe, scriptedStatus({ messages: [{ role: 'user', content: 'Page text: LEAPMUX_PDF_PAGE_49' }] }), 'pdf', source)
      await expect(pageText).rejects.toThrow('carries no typed PDF part with the exact source bytes')
      const headerOnly = expectNativeAttachmentProof(probe, scriptedStatus({ messages: [{ role: 'user', content: '%PDF-1.4 JVBERi0' }] }), 'pdf', source)
      await expect(headerOnly).rejects.toThrow('carries no typed PDF part with the exact source bytes')
    })
  })

  test('rejects a typed PDF part that carries only the PDF header', async ({ page }) => {
    await withProbePage(page, async (probe) => {
      const source = writeAttachmentFixture('pdf')
      const header = readFileSync(source).subarray(0, 8).toString('base64')
      const status = scriptedStatus({ messages: [{ role: 'user', content: [{ type: 'file', file: { filename: 'doc.pdf', file_data: `data:application/pdf;base64,${header}` } }] }] })
      await expect(expectNativeAttachmentProof(probe, status, 'pdf', source, 'openai-chat-completions')).rejects.toThrow('has 8 bytes with SHA-256')
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

  test('accepts the complete PDF in a native Chat Completions file part', async ({ page }) => {
    await withProbePage(page, async (probe) => {
      const source = writeAttachmentFixture('pdf')
      const fileData = `data:application/pdf;base64,${readFileSync(source).toString('base64')}`
      const status = scriptedStatus({ messages: [
        { role: 'system', content: 'You are a coding agent.' },
        { role: 'user', content: [{ type: 'text', text: 'Inspect the attached file.' }, { type: 'file', file: { filename: 'doc.pdf', file_data: fileData } }] },
      ] })
      await expectNativeAttachmentProof(probe, status, 'pdf', source, 'openai-chat-completions')
    })
  })

  test('accepts the complete PDF in a native Anthropic document block', async ({ page }) => {
    await withProbePage(page, async (probe) => {
      const source = writeAttachmentFixture('pdf')
      const document = { type: 'document', source: { type: 'base64', media_type: 'application/pdf', data: readFileSync(source).toString('base64') } }
      const status = scriptedStatus({ messages: [{ role: 'user', content: [{ type: 'text', text: 'Read the attached PDF.' }, document] }] }, 'anthropic-messages')
      await expectNativeAttachmentProof(probe, status, 'pdf', source, 'anthropic-messages')
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
