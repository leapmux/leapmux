import type { Page } from '@playwright/test'
import type { AttachmentKind } from './attachments'
import type { MockModelProtocol, MockModelScenarioStatus, MockModelStep } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import { Buffer } from 'node:buffer'
import { readFileSync } from 'node:fs'
import { basename } from 'node:path'
import { expect } from '@playwright/test'
import { expectAttachmentOutcome, sendWithAttachment } from './attachments'
import { assistantBubbles, expectUserMessage, sendMessage, waitForAgentIdle } from './ui'

const PDF_PAGE_MARKER = 'LEAPMUX_PDF_PAGE_49'
const CHANNEL_TOLERANCE = 48
const EXPECTED_QUADRANTS = [
  [255, 0, 0],
  [0, 255, 0],
  [0, 0, 255],
  [255, 255, 0],
]

function record(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null
}

function collectStrings(value: unknown, output: string[]): void {
  if (typeof value === 'string') {
    output.push(value)
    return
  }
  if (Array.isArray(value)) {
    for (const item of value)
      collectStrings(item, output)
    return
  }
  const object = record(value)
  if (object) {
    for (const child of Object.values(object))
      collectStrings(child, output)
  }
}

/** Read the user content of a scripted native request. */
function nativeUserStrings(body: unknown): string[] {
  const request = record(body)
  if (!request)
    return []
  const parts: unknown[] = []
  for (const field of ['messages', 'input']) {
    const rows = request[field]
    if (!Array.isArray(rows))
      continue
    for (const row of rows) {
      const message = record(row)
      if (message?.role === 'user')
        parts.push(message.content ?? message)
    }
  }
  if (typeof request.input === 'string')
    parts.push(request.input)
  const state = record(request.conversationState)
  const current = record(state?.currentMessage)
  const kiroUser = record(current?.userInputMessage)
  if (kiroUser) {
    for (const field of ['content', 'images', 'attachments', 'documents', 'documentAttachments']) {
      if (kiroUser[field] !== undefined)
        parts.push(kiroUser[field])
    }
  }
  if (request.prompt !== undefined)
    parts.push(request.prompt)
  if (request.attachments !== undefined)
    parts.push(request.attachments)

  const strings: string[] = []
  for (const part of parts)
    collectStrings(part, strings)
  return strings
}

function scriptedRequest(status: MockModelScenarioStatus, protocol?: MockModelProtocol, stepIndex = 0) {
  const request = status.requests.find(row => row.stepIndex === stepIndex)
  if (!request)
    throw new Error('the scripted attachment turn reached no native model request')
  if (protocol)
    expect(request.protocol).toBe(protocol)
  return request
}

function imageDataURIs(strings: string[]): string[] {
  const urls = new Set<string>()
  for (const value of strings) {
    for (const match of value.matchAll(/data:image\/(?:png|webp|jpeg);base64,[a-z0-9+/=]+/gi))
      urls.add(match[0])
    if (!/^[a-z0-9+/=]{80,}$/i.test(value))
      continue
    const bytes = Buffer.from(value, 'base64')
    if (bytes.subarray(0, 8).equals(Buffer.from([0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A])))
      urls.add(`data:image/png;base64,${value}`)
    else if (bytes.toString('ascii', 0, 4) === 'RIFF' && bytes.toString('ascii', 8, 12) === 'WEBP')
      urls.add(`data:image/webp;base64,${value}`)
  }
  return [...urls]
}

async function imageQuadrants(page: Page, dataURI: string): Promise<number[][]> {
  return page.evaluate(async (uri) => {
    const separator = uri.indexOf(',')
    if (separator < 0)
      throw new Error('the native image part has no base64 data')
    const mime = uri.slice(5, uri.indexOf(';'))
    const binary = atob(uri.slice(separator + 1))
    const bytes = Uint8Array.from(binary, character => character.charCodeAt(0))
    const image = await createImageBitmap(new Blob([bytes], { type: mime }))
    try {
      const canvas = document.createElement('canvas')
      canvas.width = image.width
      canvas.height = image.height
      const context = canvas.getContext('2d', { willReadFrequently: true })
      if (!context)
        throw new Error('the browser has no 2D canvas context')
      context.drawImage(image, 0, 0)
      const positions: Array<[number, number]> = [
        [0.25, 0.25],
        [0.75, 0.25],
        [0.25, 0.75],
        [0.75, 0.75],
      ]
      return positions.map(([fractionX, fractionY]) => {
        const x = Math.min(image.width - 1, Math.floor(image.width * fractionX))
        const y = Math.min(image.height - 1, Math.floor(image.height * fractionY))
        return [...context.getImageData(x, y, 1, 1).data]
      })
    }
    finally {
      image.close()
    }
  }, dataURI)
}

function hasQuadrantColors(pixels: number[][]): boolean {
  return pixels.length === EXPECTED_QUADRANTS.length && pixels.every((pixel, index) => {
    const expected = EXPECTED_QUADRANTS[index]
    return expected !== undefined
      && pixel[3] !== undefined
      && pixel[3] >= 245
      && expected.every((channel, channelIndex) => Math.abs((pixel[channelIndex] ?? -1000) - channel) <= CHANNEL_TOLERANCE)
  })
}

/** Prove the source file's content in the scripted native user request. */
export async function expectNativeAttachmentProof(
  page: Page,
  status: MockModelScenarioStatus,
  kind: AttachmentKind,
  sourcePath: string,
  protocol?: MockModelProtocol,
): Promise<void> {
  const request = scriptedRequest(status, protocol)
  const content = nativeUserStrings(request.body)
  if (content.length === 0)
    throw new Error('the native request has no user content for the attachment')
  const source = readFileSync(sourcePath)
  const joined = content.join('\n')
  if (kind === 'text') {
    expect(joined, 'the native user content must include the full text file').toContain(source.toString('utf8'))
    return
  }
  if (joined.includes(source.toString('base64')))
    return
  if (kind === 'binary')
    throw new Error('the native user content omits the complete binary file')
  if (kind === 'pdf' && joined.includes(PDF_PAGE_MARKER))
    return

  const errors: string[] = []
  for (const uri of imageDataURIs(content)) {
    try {
      if (hasQuadrantColors(await imageQuadrants(page, uri)))
        return
    }
    catch (error) {
      errors.push(error instanceof Error ? error.message : String(error))
    }
  }
  throw new Error(`the native user request does not carry the complete ${kind} file${errors.length ? `; decode errors: ${errors.join(', ')}` : ''}`)
}

/** Verify that one attachment reaches the provider's model request and the chat. */
export async function exerciseAttachmentDelivery(
  page: Page,
  modelScript: ModelScript,
  kind: AttachmentKind,
  fileName: string,
  options: { readyGroup?: string, fixturePath?: string } = {},
): Promise<void> {
  await modelScript.queue({ text: 'Attachment received.' })
  const sourcePath = await expectAttachmentOutcome(page, kind, { supported: true, fileName, ...options })
  await sendWithAttachment(page, modelScript.prompt('Inspect the attached file.'))
  const status = await modelScript.waitForSteps()
  await waitForAgentIdle(page)

  await expectNativeAttachmentProof(page, status, kind, sourcePath)
  await expectUserMessage(page, fileName)
  await expect(assistantBubbles(page).filter({ hasText: 'Attachment received.' }).first()).toBeVisible()
}

/** Send a clean turn after refusal and require the rejected bytes to stay out. */
export async function expectRefusedAttachmentsAbsent(
  page: Page,
  modelScript: ModelScript,
  rejectedPaths: string[],
  response: MockModelStep = { text: 'The clean prompt answered.' },
): Promise<void> {
  const cleanStepIndex = (await modelScript.status()).stepCount
  await modelScript.queue(response)
  await sendMessage(page, modelScript.prompt('Reply once without attachments.'))
  const status = await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  await expectNoRejectedContent(page, status, rejectedPaths, cleanStepIndex)
}

/** Check the clean request for rejected content after a refusal. */
export async function expectNoRejectedContent(page: Page, status: MockModelScenarioStatus, rejectedPaths: string[], stepIndex = status.stepCount - 1): Promise<void> {
  const body = scriptedRequest(status, undefined, stepIndex).body
  const userContent = nativeUserStrings(body).join('\n')
  const requestStrings: string[] = []
  collectStrings(body, requestStrings)
  const requestContent = requestStrings.join('\n')
  expect(userContent).toContain('Reply once without attachments.')
  const checkImages = rejectedPaths.some(path => path.endsWith('.png') || path.endsWith('.pdf'))
  const images: number[][][] = []
  if (checkImages) {
    for (const uri of imageDataURIs(requestStrings))
      images.push(await imageQuadrants(page, uri))
  }
  for (const path of rejectedPaths) {
    const source = readFileSync(path)
    expect(userContent).not.toContain(basename(path))
    expect(requestContent).not.toContain(source.toString('base64'))
    if (path.endsWith('.pdf'))
      expect(requestContent).not.toContain(PDF_PAGE_MARKER)
    if (path.endsWith('.txt'))
      expect(requestContent).not.toContain(source.toString('utf8'))
    if ((path.endsWith('.png') || path.endsWith('.pdf')) && images.some(hasQuadrantColors))
      throw new Error(`the clean request carries a rejected image derived from ${basename(path)}`)
  }
}
