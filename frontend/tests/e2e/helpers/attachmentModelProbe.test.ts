import type { Page } from '@playwright/test'
import type { AttachmentDeliveryOptions, QuadrantDecoder } from './attachmentModelProbe'
import type { AttachmentKind } from './attachments'
import type { MockModelProtocol, MockModelRequestRecord, MockModelScenarioStatus } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { NativeScenarioContext } from './nativeScenario'
import { Buffer } from 'node:buffer'
import { createHash } from 'node:crypto'
import { mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterAll, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { AMP_ACTOR_PATH_PREFIX } from './ampSurface'
import { exerciseAttachmentDelivery, expectNativeAttachmentProof, expectNativeFilePart, expectNativeImagePart, expectNativePdfPart, expectRefusedAttachmentsAbsent, nativeUserStrings, scriptedRequest } from './attachmentModelProbe'
import { writeAttachmentFixture } from './attachments'
import { CURSOR_RUN_PATH } from './cursorSurface'

// The it.each tables read the fixture when vitest collects them, which is
// before any beforeAll hook runs. So the fixture exists at module load.
const scratch = resolve(import.meta.dirname, '../../../..', '.tmp')
mkdirSync(scratch, { recursive: true })
const runDir = mkdtempSync(join(scratch, 'attachment-probe-'))
// Vitest hoists this call. The factory reads runDir only when a fixture is written.
vi.mock('./server', () => ({ getGlobalState: () => ({ tmpDir: runDir }) }))
const pdfPath = writeAttachmentFixture('pdf')
const pdf = readFileSync(pdfPath)
const encoded = pdf.toString('base64')
const pngPath = writeAttachmentFixture('image')
const png = readFileSync(pngPath)
const pngBase64 = png.toString('base64')
const binaryPath = writeAttachmentFixture('binary')
const binary = readFileSync(binaryPath)

afterAll(() => rmSync(runDir, { recursive: true, force: true }))

const NO_PDF_PART = 'carries no typed PDF part with the exact source bytes'

function nativeRequest(protocol: MockModelProtocol, body: unknown, stepIndex = 0): MockModelRequestRecord {
  return { protocol, path: '/', stepIndex, body }
}

function chat(...messages: unknown[]): MockModelRequestRecord {
  return nativeRequest('openai-chat-completions', { messages })
}

function chatFile(fileData: unknown): unknown {
  return { type: 'file', file: { filename: 'doc.pdf', file_data: fileData } }
}

function pdfDataURI(data: string): string {
  return `data:application/pdf;base64,${data}`
}

function anthropic(...messages: unknown[]): MockModelRequestRecord {
  return nativeRequest('anthropic-messages', { messages })
}

function anthropicDocument(data: unknown, mediaType = 'application/pdf'): unknown {
  return { type: 'document', source: { type: 'base64', media_type: mediaType, data } }
}

function kiro(documents: unknown, history: unknown[] = []): MockModelRequestRecord {
  return nativeRequest('aws-event-stream', { conversationState: {
    currentMessage: { userInputMessage: { content: 'Inspect the attached file.', documents } },
    history,
  } })
}

function google(...contents: unknown[]): MockModelRequestRecord {
  return nativeRequest('google-generative-language', { contents })
}

function status(...requests: MockModelRequestRecord[]): MockModelScenarioStatus {
  return { complete: true, nextStep: requests.length, stepCount: requests.length, ruleMatches: {}, pendingGates: [], unexpectedRequests: [], requests }
}

function responses(...input: unknown[]): MockModelRequestRecord {
  return nativeRequest('openai-responses', { input })
}

function pngDataURI(data: string): string {
  return `data:image/png;base64,${data}`
}

function chatImage(url: unknown): unknown {
  return { type: 'image_url', image_url: { url } }
}

function anthropicImage(data: unknown, mediaType = 'image/png'): unknown {
  return { type: 'image', source: { type: 'base64', media_type: mediaType, data } }
}

function responsesImage(url: unknown): unknown {
  return { type: 'input_image', image_url: url }
}

function kiroImages(images: unknown, history: unknown[] = []): MockModelRequestRecord {
  return nativeRequest('aws-event-stream', { conversationState: {
    currentMessage: { userInputMessage: { content: 'Inspect the attached file.', images } },
    history,
  } })
}

/**
 * A page that fails each browser call.
 *
 * Each request below carries the complete PNG base64 somewhere. A proof that
 * accepts it therefore decides without a decoded pixel, and a proof that
 * rejects it must not depend on the browser either.
 */
const noBrowser = Object.assign({} as Page, {
  evaluate: async (): Promise<never> => {
    throw new Error('the unit test has no browser to decode an image')
  },
})

function imageProof(request: MockModelRequestRecord): Promise<void> {
  return expectNativeAttachmentProof(noBrowser, status(request), 'image', pngPath)
}

/** The pixels that a browser decodes from the quadrant fixture, in reading order, with full alpha. */
const SOURCE_QUADRANTS = [[255, 0, 0, 255], [0, 255, 0, 255], [0, 0, 255, 255], [255, 255, 0, 255]]

/**
 * Bytes that start with a WebP signature. A unit test decodes no pixels, so the
 * signature is all that the format check reads. The browser suite in
 * attachmentModelProbe.spec.ts decodes a real WebP.
 */
const webp = Buffer.concat([Buffer.from('RIFF', 'latin1'), Buffer.from([0x1A, 0, 0, 0]), Buffer.from('WEBPVP8L', 'latin1'), Buffer.alloc(18, 0x2F)])
const webpBase64 = webp.toString('base64')
const webpURI = `data:image/webp;base64,${webpBase64}`

/** The decoder of the exact-byte proof, which must never decode an image. */
const noDecode: QuadrantDecoder = async () => {
  throw new Error('the exact-byte proof decoded an image')
}

/** A decoder that knows one data URI. It returns `pixels` for that URI and fails for any other. */
function decoderFor(dataURI: string, pixels: number[][] = SOURCE_QUADRANTS): QuadrantDecoder {
  return async (uri) => {
    if (uri !== dataURI)
      throw new Error(`the proof decoded an unexpected data URI of ${uri.length} characters`)
    return pixels
  }
}

function sha256Hex(bytes: Buffer): string {
  return createHash('sha256').update(bytes).digest('hex')
}

/** Amp's thread content as the mock records it on Amp's actor route. */
function amp(...messages: unknown[]): MockModelRequestRecord {
  return { protocol: 'anthropic-messages', path: AMP_ACTOR_PATH_PREFIX, stepIndex: 0, body: { messages } }
}

/** An image block in Amp's own shape, which spells the media type `mediaType` and adds `sourcePath`. */
function ampImage(data: unknown, mediaType = 'image/png'): unknown {
  return { type: 'image', sourcePath: 'stream-json://stdin/line-1/image-1', source: { type: 'base64', mediaType, data } }
}

function cursor(attachments: unknown): MockModelRequestRecord {
  return { protocol: 'openai-responses', path: CURSOR_RUN_PATH, stepIndex: 0, body: { prompt: 'Inspect the attached file.', attachments, conversationId: 'conversation-1' } }
}

/** Another valid PDF with the same page marker: the fixture with a later version header. */
function sameMarkerPdf(): Buffer {
  return Buffer.from(pdf.toString('latin1').replace('%PDF-1.4', '%PDF-1.5'), 'latin1')
}

describe('nativeUserStrings', () => {
  it('reads Google user bytes without model media, tool results, or instructions', () => {
    const body = {
      systemInstruction: { parts: [{ text: 'SYSTEM_ONLY' }] },
      tools: [{ functionDeclarations: [{ description: 'SCHEMA_ONLY' }] }],
      contents: [
        { role: 'model', parts: [{ inlineData: { mimeType: 'image/png', data: 'MODEL_ONLY' } }] },
        { role: 'user', parts: [{ text: 'ACTUAL_USER' }, { inlineData: { mimeType: 'audio/wav', data: 'ACTUAL_BYTES' } }] },
        { role: 'user', parts: [{ functionResponse: { response: { data: 'RESULT_ONLY' } } }] },
      ],
    }
    expect(nativeUserStrings(body)).toEqual(['ACTUAL_USER', 'audio/wav', 'ACTUAL_BYTES'])
  })

  it.each([undefined, null, false, 0, '', {}, { contents: null }, { contents: [null, {}, { role: 'user', parts: null }] }])('handles absent or malformed native user contents: %j', (body) => {
    expect(nativeUserStrings(body)).toEqual([])
  })

  it('preserves the existing generic API user content reader', () => {
    expect(nativeUserStrings({ messages: [{ role: 'system', content: 'SYSTEM_ONLY' }, { role: 'user', content: [{ type: 'text', text: 'ACTUAL_USER' }, { type: 'image_url', image_url: { url: 'ACTUAL_IMAGE' } }] }] }))
      .toEqual(['text', 'ACTUAL_USER', 'image_url', 'ACTUAL_IMAGE'])
  })
})

describe('expectNativePdfPart', () => {
  it('uses a fixture whose base64 has padding and a plus sign, so each non-canonical form below differs from it', () => {
    expect(encoded.endsWith('=')).toBe(true)
    expect(encoded).toContain('+')
    expect(pdf.toString('latin1')).toContain('LEAPMUX_PDF_PAGE_49')
  })

  describe('accepts the exact PDF in a typed PDF part of the current user turn', () => {
    it('accepts an Anthropic document block', () => {
      expect(() => expectNativePdfPart(anthropic(
        { role: 'user', content: [{ type: 'text', text: 'Read the attached PDF.' }, anthropicDocument(encoded)] },
      ), pdf)).not.toThrow()
    })

    it('accepts a Chat Completions file part with a file name', () => {
      expect(() => expectNativePdfPart(chat(
        { role: 'system', content: 'You are a coding agent.' },
        { role: 'user', content: [{ type: 'text', text: 'Read the attached PDF.' }, chatFile(pdfDataURI(encoded))] },
      ), pdf)).not.toThrow()
    })

    it('accepts a Chat Completions file part without a file name', () => {
      expect(() => expectNativePdfPart(chat(
        { role: 'user', content: [{ type: 'text', text: 'Read this document.' }, { type: 'file', file: { file_data: pdfDataURI(encoded) } }] },
      ), pdf)).not.toThrow()
    })

    it('accepts a Kiro document of the current user input message', () => {
      expect(() => expectNativePdfPart(kiro([{ name: 'kiro-doc', format: 'pdf', source: { bytes: encoded } }]), pdf)).not.toThrow()
    })

    it('accepts a Google inlineData part', () => {
      expect(() => expectNativePdfPart(google(
        { role: 'user', parts: [{ text: 'Read the attached PDF.' }, { inlineData: { mimeType: 'application/pdf', data: encoded } }] },
      ), pdf)).not.toThrow()
    })

    it('accepts a part in a later user message of the same turn', () => {
      expect(() => expectNativePdfPart(chat(
        { role: 'user', content: '<system-reminder>context</system-reminder>' },
        { role: 'user', content: [chatFile(pdfDataURI(encoded))] },
      ), pdf)).not.toThrow()
    })

    it('accepts the part when an instruction row follows the user row, as Claude Code 2.1 sends its environment', () => {
      expect(() => expectNativePdfPart(anthropic(
        { role: 'user', content: [{ type: 'text', text: 'Read the attached PDF.' }, anthropicDocument(encoded)] },
        { role: 'system', content: [{ type: 'text', text: '# Environment' }] },
      ), pdf)).not.toThrow()
      expect(() => expectNativePdfPart(chat(
        { role: 'user', content: [chatFile(pdfDataURI(encoded))] },
        { role: 'developer', content: 'Follow the project rules.' },
      ), pdf)).not.toThrow()
    })

    it('accepts the exact part beside another typed part that carries different bytes', () => {
      expect(() => expectNativePdfPart(kiro([
        { name: 'header', format: 'pdf', source: { bytes: pdf.subarray(0, 8).toString('base64') } },
        { name: 'kiro-doc', format: 'pdf', source: { bytes: encoded } },
      ]), pdf)).not.toThrow()
    })
  })

  describe('rejects a conversion sample without the original PDF bytes', () => {
    it('rejects the page marker in user text', () => {
      expect(() => expectNativePdfPart(chat({ role: 'user', content: 'LEAPMUX_PDF_PAGE_49' }), pdf)).toThrow(NO_PDF_PART)
      expect(() => expectNativePdfPart(anthropic({ role: 'user', content: [{ type: 'text', text: 'Page text: LEAPMUX_PDF_PAGE_49' }] }), pdf)).toThrow(NO_PDF_PART)
    })

    it('rejects an unrelated image with the four quadrant colors', () => {
      const raster = `data:image/png;base64,${png.toString('base64')}`
      expect(() => expectNativePdfPart(chat({ role: 'user', content: [{ type: 'image_url', image_url: { url: raster } }] }), pdf)).toThrow(NO_PDF_PART)
      expect(() => expectNativePdfPart(anthropic({ role: 'user', content: [{ type: 'image', source: { type: 'base64', media_type: 'image/png', data: png.toString('base64') } }] }), pdf)).toThrow(NO_PDF_PART)
      expect(() => expectNativePdfPart(google({ role: 'user', parts: [{ inlineData: { mimeType: 'image/png', data: png.toString('base64') } }] }), pdf)).toThrow('declares mimeType=image/png, not PDF')
    })

    it('rejects an Anthropic document whose source is extracted text', () => {
      const request = anthropic({ role: 'user', content: [{ type: 'document', source: { type: 'text', media_type: 'text/plain', data: 'LEAPMUX_PDF_PAGE_49' } }] })
      expect(() => expectNativePdfPart(request, pdf)).toThrow('messages[0].content[0] has a source of type text, not base64 bytes')
    })
  })

  describe('rejects the complete PDF bytes outside a typed PDF part', () => {
    it('rejects the base64 as plain user text', () => {
      expect(() => expectNativePdfPart(chat({ role: 'user', content: encoded }), pdf)).toThrow(NO_PDF_PART)
      expect(() => expectNativePdfPart(chat({ role: 'user', content: [{ type: 'text', text: encoded }] }), pdf)).toThrow(NO_PDF_PART)
      expect(() => expectNativePdfPart(anthropic({ role: 'user', content: [{ type: 'text', text: encoded }] }), pdf)).toThrow(NO_PDF_PART)
    })

    it('rejects a complete PDF data URI inside a text part or an image part', () => {
      expect(() => expectNativePdfPart(chat({ role: 'user', content: [{ type: 'text', text: pdfDataURI(encoded) }] }), pdf)).toThrow(NO_PDF_PART)
      expect(() => expectNativePdfPart(chat({ role: 'user', content: [{ type: 'image_url', image_url: { url: pdfDataURI(encoded) } }] }), pdf)).toThrow(NO_PDF_PART)
    })

    it('rejects the base64 in the Kiro user text with no document', () => {
      const request = nativeRequest('aws-event-stream', { conversationState: { currentMessage: { userInputMessage: { content: encoded } } } })
      expect(() => expectNativePdfPart(request, pdf)).toThrow(NO_PDF_PART)
    })
  })

  describe('rejects a typed part that declares another type', () => {
    it.each([
      ['an Anthropic document', anthropic({ role: 'user', content: [anthropicDocument(encoded, 'application/octet-stream')] }), 'declares media_type=application/octet-stream, not PDF'],
      ['a Chat Completions file part', chat({ role: 'user', content: [chatFile(`data:application/octet-stream;base64,${encoded}`)] }), 'declares file.file_data=data:application/octet-stream, not PDF'],
      ['a Chat Completions file part with an image type', chat({ role: 'user', content: [chatFile(`data:image/png;base64,${encoded}`)] }), 'declares file.file_data=data:image/png, not PDF'],
      ['a Kiro document', kiro([{ name: 'doc', format: 'docx', source: { bytes: encoded } }]), 'declares format=docx, not PDF'],
      ['a Kiro document with no format', kiro([{ name: 'doc', source: { bytes: encoded } }]), 'declares format=undefined, not PDF'],
      ['a Google inlineData part', google({ role: 'user', parts: [{ inlineData: { mimeType: 'application/octet-stream', data: encoded } }] }), 'declares mimeType=application/octet-stream, not PDF'],
    ])('rejects %s', (_name, request, message) => {
      expect(() => expectNativePdfPart(request, pdf)).toThrow(message)
    })

    it('rejects a MIME type that differs only in case', () => {
      expect(() => expectNativePdfPart(chat({ role: 'user', content: [chatFile(`data:Application/PDF;base64,${encoded}`)] }), pdf)).toThrow('not PDF')
    })
  })

  describe('rejects a Chat Completions file part that carries no base64 data URI', () => {
    it.each([
      ['raw base64', encoded, 'has a file.file_data that is not of the form data:<type>;base64,<data>'],
      ['a data URI without the base64 marker', `data:application/pdf,${encoded}`, 'has a file.file_data that is not of the form data:<type>;base64,<data>'],
      ['a data URI with a media type parameter', `data:application/pdf;name=doc.pdf;base64,${encoded}`, 'has a file.file_data that is not of the form data:<type>;base64,<data>'],
      ['a URL', 'https://example.com/doc.pdf', 'has a file.file_data that is not of the form data:<type>;base64,<data>'],
      ['an empty string', '', 'has a file.file_data that is not of the form data:<type>;base64,<data>'],
      ['null', null, 'has null in file.file_data, not a base64 data URI'],
      ['a number', 42, 'has a number in file.file_data, not a base64 data URI'],
      ['an object', { data: encoded }, 'has an object in file.file_data, not a base64 data URI'],
    ])('rejects %s', (_name, fileData, message) => {
      expect(() => expectNativePdfPart(chat({ role: 'user', content: [chatFile(fileData)] }), pdf)).toThrow(message)
    })

    it('rejects an uploaded file reference and an absent file object', () => {
      expect(() => expectNativePdfPart(chat({ role: 'user', content: [{ type: 'file', file: { file_id: 'file-abc' } }] }), pdf)).toThrow('has no value in file.file_data')
      expect(() => expectNativePdfPart(chat({ role: 'user', content: [{ type: 'file' }] }), pdf)).toThrow('has no value in file.file_data')
    })

    it('rejects a PDF data URI with an empty payload', () => {
      expect(() => expectNativePdfPart(chat({ role: 'user', content: [chatFile(pdfDataURI(''))] }), pdf)).toThrow('has empty data')
    })
  })

  describe('rejects empty, null, and non-string data', () => {
    it.each([
      ['an empty string', '', 'has empty data'],
      ['null', null, 'has null in place of base64 bytes'],
      ['an absent value', undefined, 'has no value in place of base64 bytes'],
      ['a number', 0, 'has a number in place of base64 bytes'],
      ['an object', { bytes: encoded }, 'has an object in place of base64 bytes'],
      ['an array', [...pdf], 'has an array in place of base64 bytes'],
    ])('rejects %s in an Anthropic document', (_name, data, message) => {
      expect(() => expectNativePdfPart(anthropic({ role: 'user', content: [anthropicDocument(data)] }), pdf)).toThrow(message)
    })

    it.each([
      ['null', null, 'has null in place of base64 bytes'],
      ['an empty string', '', 'has empty data'],
    ])('rejects %s in Kiro document bytes', (_name, bytes, message) => {
      expect(() => expectNativePdfPart(kiro([{ name: 'doc', format: 'pdf', source: { bytes } }]), pdf)).toThrow(message)
    })

    it('rejects a Kiro document with no source', () => {
      expect(() => expectNativePdfPart(kiro([{ name: 'doc', format: 'pdf' }]), pdf)).toThrow('has no value in place of base64 bytes')
    })

    it('rejects a Google inlineData part with no data', () => {
      expect(() => expectNativePdfPart(google({ role: 'user', parts: [{ inlineData: { mimeType: 'application/pdf' } }] }), pdf)).toThrow('has no value in place of base64 bytes')
    })
  })

  describe('rejects bytes that differ from the source', () => {
    it('rejects the PDF header alone', () => {
      const header = pdf.subarray(0, 8).toString('base64')
      expect(() => expectNativePdfPart(anthropic({ role: 'user', content: [anthropicDocument(header)] }), pdf))
        .toThrow(/has 8 bytes with SHA-256 [0-9a-f]{64}, not the 740-byte source with SHA-256 c3ca84e7858231d16759b15431cbfd39aafe0ed0738da79a2af43744a26e6b4f/)
    })

    it('rejects the PDF without its last byte', () => {
      const truncated = pdf.subarray(0, pdf.length - 1).toString('base64')
      expect(() => expectNativePdfPart(chat({ role: 'user', content: [chatFile(pdfDataURI(truncated))] }), pdf)).toThrow(`has ${pdf.length - 1} bytes`)
    })

    it('rejects the PDF with one changed byte', () => {
      const corrupt = Buffer.from(pdf)
      corrupt[pdf.length - 2] = corrupt[pdf.length - 2]! ^ 0x01
      expect(() => expectNativePdfPart(kiro([{ name: 'doc', format: 'pdf', source: { bytes: corrupt.toString('base64') } }]), pdf)).toThrow(`has ${pdf.length} bytes with SHA-256`)
    })

    it('rejects the PDF with extra bytes after it', () => {
      const extended = Buffer.concat([pdf, Buffer.from('\n')]).toString('base64')
      expect(() => expectNativePdfPart(google({ role: 'user', parts: [{ inlineData: { mimeType: 'application/pdf', data: extended } }] }), pdf)).toThrow(`has ${pdf.length + 1} bytes`)
    })

    it('rejects another file that holds the same page marker', () => {
      const other = sameMarkerPdf()
      expect(other.equals(pdf)).toBe(false)
      expect(other.toString('latin1')).toContain('LEAPMUX_PDF_PAGE_49')
      expect(() => expectNativePdfPart(anthropic({ role: 'user', content: [anthropicDocument(other.toString('base64'))] }), pdf)).toThrow('bytes with SHA-256')
    })

    it.each([
      ['without its padding', () => encoded.replace(/=+$/, '')],
      ['in the URL-safe alphabet', () => encoded.replaceAll('+', '-').replaceAll('/', '_')],
      ['with a line break every 76 characters', () => encoded.replace(/(.{76})/g, '$1\n')],
      ['with surrounding white space', () => ` ${encoded} `],
    ])('rejects the complete PDF base64 %s', (_name, form) => {
      const data = form()
      expect(data).not.toBe(encoded)
      expect(Buffer.from(data, 'base64').equals(pdf)).toBe(true)
      expect(() => expectNativePdfPart(anthropic({ role: 'user', content: [anthropicDocument(data)] }), pdf)).toThrow('has data that is not canonical base64')
    })
  })

  describe('rejects the exact PDF outside the current user turn', () => {
    it('rejects a system message', () => {
      expect(() => expectNativePdfPart(chat(
        { role: 'system', content: [chatFile(pdfDataURI(encoded))] },
        { role: 'user', content: 'Read the attached PDF.' },
      ), pdf)).toThrow(NO_PDF_PART)
    })

    it('rejects a trailing instruction row that carries the PDF', () => {
      expect(() => expectNativePdfPart(anthropic(
        { role: 'user', content: [{ type: 'text', text: 'Read the attached PDF.' }] },
        { role: 'system', content: [anthropicDocument(encoded)] },
      ), pdf)).toThrow(NO_PDF_PART)
    })

    it('rejects an earlier turn that an assistant row ends, even when an instruction row follows the current user row', () => {
      expect(() => expectNativePdfPart(anthropic(
        { role: 'user', content: [anthropicDocument(encoded)] },
        { role: 'assistant', content: [{ type: 'text', text: 'Read.' }] },
        { role: 'user', content: [{ type: 'text', text: 'Summarize it again.' }] },
        { role: 'system', content: [{ type: 'text', text: '# Environment' }] },
      ), pdf)).toThrow(NO_PDF_PART)
    })

    it('rejects the Anthropic system field', () => {
      const request = nativeRequest('anthropic-messages', { system: [anthropicDocument(encoded)], messages: [{ role: 'user', content: 'Read the attached PDF.' }] })
      expect(() => expectNativePdfPart(request, pdf)).toThrow(NO_PDF_PART)
    })

    it('rejects the Google system instruction', () => {
      const request = nativeRequest('google-generative-language', {
        systemInstruction: { parts: [{ inlineData: { mimeType: 'application/pdf', data: encoded } }] },
        contents: [{ role: 'user', parts: [{ text: 'Read the attached PDF.' }] }],
      })
      expect(() => expectNativePdfPart(request, pdf)).toThrow(NO_PDF_PART)
    })

    it('rejects an assistant message that ends the history', () => {
      expect(() => expectNativePdfPart(chat({ role: 'assistant', content: [chatFile(pdfDataURI(encoded))] }), pdf)).toThrow(NO_PDF_PART)
    })

    it('rejects a document inside an Anthropic tool result, which a file read produces', () => {
      const request = anthropic({ role: 'user', content: [{ type: 'tool_result', tool_use_id: 'toolu_1', content: [anthropicDocument(encoded)] }] })
      expect(() => expectNativePdfPart(request, pdf)).toThrow(NO_PDF_PART)
    })

    it('rejects a file part in the user row that follows a tool row, the synthetic carrier that Copilot and Junie build for a tool\'s file', () => {
      expect(() => expectNativePdfPart(chat(
        { role: 'user', content: 'Read the plan.' },
        { role: 'assistant', content: null, tool_calls: [{ id: 'call_1', type: 'function', function: { name: 'view', arguments: '{}' } }] },
        { role: 'tool', tool_call_id: 'call_1', content: 'viewed' },
        { role: 'user', content: [chatFile(pdfDataURI(encoded))] },
      ), pdf)).toThrow(NO_PDF_PART)
    })

    it('rejects the same synthetic carrier when a genuine user row follows it in the same turn', () => {
      expect(() => expectNativePdfPart(chat(
        { role: 'user', content: 'Read the plan.' },
        { role: 'assistant', content: null, tool_calls: [{ id: 'call_1', type: 'function', function: { name: 'view', arguments: '{}' } }] },
        { role: 'tool', tool_call_id: 'call_1', content: 'viewed' },
        { role: 'user', content: [chatFile(pdfDataURI(encoded))] },
        { role: 'user', content: 'Summarize what you saw.' },
      ), pdf)).toThrow(NO_PDF_PART)
    })

    it('accepts the part in a user row that follows the assistant row that answered a tool call', () => {
      expect(() => expectNativePdfPart(chat(
        { role: 'user', content: 'Read the plan.' },
        { role: 'assistant', content: null, tool_calls: [{ id: 'call_1', type: 'function', function: { name: 'view', arguments: '{}' } }] },
        { role: 'tool', tool_call_id: 'call_1', content: 'viewed' },
        { role: 'assistant', content: 'Viewed.' },
        { role: 'user', content: [chatFile(pdfDataURI(encoded))] },
      ), pdf)).not.toThrow()
    })

    it('rejects a document beside an Anthropic tool result in the same message', () => {
      const request = anthropic({ role: 'user', content: [{ type: 'tool_result', tool_use_id: 'toolu_1', content: 'Read the file.' }, anthropicDocument(encoded)] })
      expect(() => expectNativePdfPart(request, pdf)).toThrow(NO_PDF_PART)
    })

    it('rejects a Google inlineData part beside a functionResponse in the same content', () => {
      const request = google({ role: 'user', parts: [{ functionResponse: { id: 'call-1', name: 'read_file', response: { output: 'read' } } }, { inlineData: { mimeType: 'application/pdf', data: encoded } }] })
      expect(() => expectNativePdfPart(request, pdf)).toThrow(NO_PDF_PART)
    })

    it('rejects a Chat Completions file that only an earlier turn carries', () => {
      expect(() => expectNativePdfPart(chat(
        { role: 'user', content: [chatFile(pdfDataURI(encoded))] },
        { role: 'assistant', content: 'Read it.' },
        { role: 'user', content: 'Reply once without attachments.' },
      ), pdf)).toThrow(NO_PDF_PART)
    })

    it('rejects an Anthropic document that only an earlier turn carries', () => {
      expect(() => expectNativePdfPart(anthropic(
        { role: 'user', content: [anthropicDocument(encoded)] },
        { role: 'assistant', content: [{ type: 'text', text: 'Read it.' }] },
        { role: 'user', content: [{ type: 'text', text: 'Reply once without attachments.' }] },
      ), pdf)).toThrow(NO_PDF_PART)
    })

    it('rejects a Kiro document that only the history carries', () => {
      const earlier = { userInputMessage: { content: 'Earlier.', documents: [{ name: 'kiro-doc', format: 'pdf', source: { bytes: encoded } }] } }
      expect(() => expectNativePdfPart(kiro([], [earlier]), pdf)).toThrow(NO_PDF_PART)
    })

    it('rejects a Google inlineData part that only an earlier turn carries', () => {
      expect(() => expectNativePdfPart(google(
        { role: 'user', parts: [{ inlineData: { mimeType: 'application/pdf', data: encoded } }] },
        { role: 'model', parts: [{ text: 'Read it.' }] },
        { role: 'user', parts: [{ text: 'Reply once without attachments.' }] },
      ), pdf)).toThrow(NO_PDF_PART)
    })
  })

  it('states the location and the defect of each typed part that comes close', () => {
    const request = kiro([
      { name: 'one', format: 'docx', source: { bytes: encoded } },
      { name: 'two', format: 'pdf', source: { bytes: pdf.subarray(0, 8).toString('base64') } },
    ])
    expect(() => expectNativePdfPart(request, pdf)).toThrow(
      'the current user turn of the scripted aws-event-stream request carries no typed PDF part with the exact source bytes: '
      + 'conversationState.currentMessage.userInputMessage.documents[0] declares format=docx, not PDF; '
      + 'conversationState.currentMessage.userInputMessage.documents[1] has 8 bytes with SHA-256',
    )
  })

  it.each([
    undefined,
    null,
    'text',
    {},
    { messages: null },
    { messages: [null, {}, { role: 'user', content: null }, { role: 'user', content: 'text' }] },
    { messages: [{ role: 'user', content: [null, 'text', { type: 'document', source: null }] }] },
  ])('rejects an absent or malformed Anthropic body: %j', (body) => {
    expect(() => expectNativePdfPart(nativeRequest('anthropic-messages', body), pdf)).toThrow('the current user turn of the scripted anthropic-messages request')
  })

  it.each([
    undefined,
    null,
    { conversationState: null },
    { conversationState: { currentMessage: { userInputMessage: { documents: null } } } },
    { conversationState: { currentMessage: { userInputMessage: { documents: [null] } } } },
  ])('rejects an absent or malformed Kiro body: %j', (body) => {
    expect(() => expectNativePdfPart(nativeRequest('aws-event-stream', body), pdf)).toThrow('the current user turn of the scripted aws-event-stream request')
  })

  it.each([
    undefined,
    { contents: null },
    { contents: [null, { role: 'user', parts: null }, { role: 'user', parts: [null, { inlineData: null }] }] },
  ])('rejects an absent or malformed Google body: %j', (body) => {
    expect(() => expectNativePdfPart(nativeRequest('google-generative-language', body), pdf)).toThrow(NO_PDF_PART)
  })

  it('states that no file route uses the Responses protocol', () => {
    const request = nativeRequest('openai-responses', { input: [{ role: 'user', content: [{ type: 'input_file', file_data: pdfDataURI(encoded) }] }] })
    expect(() => expectNativePdfPart(request, pdf)).toThrow('no provider that sends a typed file part uses the openai-responses protocol')
  })
})

describe('expectNativeFilePart', () => {
  const wav = Buffer.concat([Buffer.from('RIFF', 'latin1'), Buffer.from([0x24, 0, 0, 0]), Buffer.from('WAVEfmt ', 'latin1')])
  const wavBase64 = wav.toString('base64')

  it('accepts the exact bytes of a non-PDF type in each typed file shape', () => {
    expect(() => expectNativeFilePart(google({ role: 'user', parts: [{ text: 'Read the valid WAV.' }, { inlineData: { mimeType: 'audio/wav', data: wavBase64 } }] }), wav, 'audio/wav')).not.toThrow()
    expect(() => expectNativeFilePart(chat({ role: 'user', content: [chatFile(`data:audio/wav;base64,${wavBase64}`)] }), wav, 'audio/wav')).not.toThrow()
    expect(() => expectNativeFilePart(anthropic({ role: 'user', content: [anthropicDocument(wavBase64, 'audio/wav')] }), wav, 'audio/wav')).not.toThrow()
  })

  it('fails a part that declares another media type', () => {
    expect(() => expectNativeFilePart(google({ role: 'user', parts: [{ inlineData: { mimeType: 'audio/x-wav', data: wavBase64 } }] }), wav, 'audio/wav'))
      .toThrow('contents[0].parts[0] declares mimeType=audio/x-wav, not audio/wav')
  })

  it('fails a data URI with a media type parameter', () => {
    expect(() => expectNativeFilePart(chat({ role: 'user', content: [chatFile(`data:audio/wav;codecs=1;base64,${wavBase64}`)] }), wav, 'audio/wav'))
      .toThrow('has a file.file_data that is not of the form data:<type>;base64,<data>')
  })

  it('fails the exact bytes outside a typed part, and different bytes inside one', () => {
    expect(() => expectNativeFilePart(chat({ role: 'user', content: wavBase64 }), wav, 'audio/wav'))
      .toThrow('carries no typed audio/wav part with the exact source bytes')
    expect(() => expectNativeFilePart(chat({ role: 'user', content: [chatFile(`data:audio/wav;base64,${wav.subarray(0, 4).toString('base64')}`)] }), wav, 'audio/wav'))
      .toThrow('has 4 bytes with SHA-256')
  })

  it('accepts a declared type that an anchored pattern matches, and fails one that it does not match', () => {
    const request = chat({ role: 'user', content: [chatFile(`data:application/octet-stream;base64,${wavBase64}`)] })
    expect(() => expectNativeFilePart(request, wav, /^application\//)).not.toThrow()
    expect(() => expectNativeFilePart(request, wav, /^audio\//)).toThrow('declares file.file_data=data:application/octet-stream, not /^audio\\//')
  })

  it('refuses an empty media type and a pattern that keeps a match position', () => {
    const request = chat({ role: 'user', content: [chatFile(`data:audio/wav;base64,${wavBase64}`)] })
    expect(() => expectNativeFilePart(request, wav, '')).toThrow('needs the media type that the part declares')
    expect(() => expectNativeFilePart(request, wav, /^audio\//g)).toThrow('without the g or y flag')
    expect(() => expectNativeFilePart(request, wav, /^audio\//y)).toThrow('without the g or y flag')
  })
})

describe('expectNativeImagePart', () => {
  const NO_IMAGE_PART = 'does not carry the complete image file in a typed image part with the exact source bytes'

  it('uses a fixture whose base64 has padding and a slash, so each non-canonical form below differs from it', () => {
    expect(pngBase64.endsWith('=')).toBe(true)
    expect(pngBase64).toContain('/')
    expect(png.subarray(0, 8)).toEqual(Buffer.from([0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A]))
  })

  describe('accepts the exact PNG in a typed image part of the current user turn', () => {
    it.each([
      ['an Anthropic image block', anthropic({ role: 'user', content: [{ type: 'text', text: 'Inspect the attached file.' }, anthropicImage(pngBase64)] })],
      ['a Chat Completions image part with a detail level', chat(
        { role: 'system', content: 'You are a coding agent.' },
        { role: 'user', content: [{ type: 'text', text: 'Inspect the attached file.' }, { type: 'image_url', image_url: { url: pngDataURI(pngBase64), detail: 'auto' } }] },
      )],
      ['a Responses input image', responses(
        { type: 'message', role: 'developer', content: [{ type: 'input_text', text: 'Follow the project rules.' }] },
        { type: 'message', role: 'user', content: [{ type: 'input_text', text: 'Inspect the attached file.' }, { type: 'input_image', image_url: pngDataURI(pngBase64), detail: 'auto' }] },
      )],
      ['a Cursor context image', cursor([{ kind: 'image', data: pngBase64, mimeType: 'image/png' }])],
      ['an Amp thread image in Amp\'s own block shape', amp({ role: 'user', content: [{ type: 'text', text: 'Inspect the attached file.' }, ampImage(pngBase64)] })],
      ['a Kiro image of the current user input message', kiroImages([{ format: 'png', source: { bytes: pngBase64 } }])],
      ['a Google inlineData part', google({ role: 'user', parts: [{ text: 'Inspect the attached file.' }, { inlineData: { mimeType: 'image/png', data: pngBase64 } }] })],
    ])('accepts %s', async (_name, request) => {
      await expect(expectNativeImagePart(request, png, noDecode)).resolves.toBeUndefined()
    })

    it('accepts a part in a later user message of the same turn', async () => {
      await expect(expectNativeImagePart(chat(
        { role: 'user', content: '<system-reminder>context</system-reminder>' },
        { role: 'user', content: [chatImage(pngDataURI(pngBase64))] },
      ), png, noDecode)).resolves.toBeUndefined()
    })

    it('accepts the part when an instruction row follows the user row, as Claude Code 2.1 sends its environment', async () => {
      await expect(expectNativeImagePart(anthropic(
        { role: 'user', content: [{ type: 'text', text: 'Inspect the attached file.' }, anthropicImage(pngBase64)] },
        { role: 'system', content: [{ type: 'text', text: '# Environment' }] },
      ), png, noDecode)).resolves.toBeUndefined()
    })

    it('accepts the current turn after an earlier turn that a tool call ended', async () => {
      await expect(expectNativeImagePart(anthropic(
        { role: 'user', content: [{ type: 'text', text: 'List the files.' }] },
        { role: 'assistant', content: [{ type: 'tool_use', id: 'toolu_1', name: 'Bash', input: {} }] },
        { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'toolu_1', content: 'shot.png' }] },
        { role: 'assistant', content: [{ type: 'text', text: 'One file.' }] },
        { role: 'user', content: [{ type: 'text', text: 'Inspect the attached file.' }, anthropicImage(pngBase64)] },
      ), png, noDecode)).resolves.toBeUndefined()
    })

    it('accepts the exact part beside another typed part that carries different bytes', async () => {
      await expect(expectNativeImagePart(kiroImages([
        { format: 'png', source: { bytes: png.subarray(0, 8).toString('base64') } },
        { format: 'png', source: { bytes: pngBase64 } },
      ]), png, noDecode)).resolves.toBeUndefined()
    })

    it('accepts another PNG fixture, such as the 64x64 tool image that the Kiro and Grok specs attach', async () => {
      const tool = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAEAAAABACAYAAACqaXHeAAAAb0lEQVR42u3YMREAIAwEwZcYicjBFSigykwatjgDW15S63wdAAAAAAAAAODdTi8AAAAAAAAAAAAAAAAAAAAAgCECAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAJjqArCUycOeoJLSAAAAAElFTkSuQmCC', 'base64')
      await expect(expectNativeImagePart(kiroImages([{ format: 'png', source: { bytes: tool.toString('base64') } }]), tool, noDecode)).resolves.toBeUndefined()
      await expect(expectNativeImagePart(kiroImages([{ format: 'png', source: { bytes: pngBase64 } }]), tool, noDecode)).rejects.toThrow('not the 168-byte source')
    })
  })

  describe('reads each route by its own block shape', () => {
    it('rejects an Anthropic-shaped block on Amp\'s route, which Amp never sends', async () => {
      await expect(expectNativeImagePart(amp({ role: 'user', content: [anthropicImage(pngBase64)] }), png, noDecode)).rejects.toThrow('mediaType=undefined')
    })

    it('rejects an Amp-shaped block on the Anthropic route', async () => {
      await expect(expectNativeImagePart(anthropic({ role: 'user', content: [ampImage(pngBase64)] }), png, noDecode)).rejects.toThrow('media_type=undefined')
    })

    it('rejects an Amp image beside a tool result, and an Amp image of another type', async () => {
      await expect(expectNativeImagePart(amp({ role: 'user', content: [{ type: 'tool_result', tool_use_id: 'toolu_1', content: 'Read.' }, ampImage(pngBase64)] }), png, noDecode)).rejects.toThrow(NO_IMAGE_PART)
      await expect(expectNativeImagePart(amp({ role: 'user', content: [ampImage(pngBase64, 'image/jpeg')] }), png, noDecode)).rejects.toThrow('mediaType=image/jpeg')
    })
  })

  describe('rejects a typed part that declares another type than the source', () => {
    it.each([
      ['an Anthropic image', anthropic({ role: 'user', content: [anthropicImage(pngBase64, 'image/jpeg')] }), 'messages[0].content[0] declares media_type=image/jpeg, not image/png'],
      ['a Chat Completions image part', chat({ role: 'user', content: [chatImage(`data:application/octet-stream;base64,${pngBase64}`)] }), 'messages[0].content[0] declares image_url.url=data:application/octet-stream, not image/png'],
      ['a Responses input image', responses({ role: 'user', content: [responsesImage(`data:image/webp;base64,${pngBase64}`)] }), 'input[0].content[0] declares image_url=data:image/webp, not image/png'],
      ['a Cursor context image', cursor([{ kind: 'image', data: pngBase64, mimeType: 'image/gif' }]), 'attachments[0] declares mimeType=image/gif, not image/png'],
      ['a Cursor context image with no media type', cursor([{ kind: 'image', data: pngBase64 }]), 'attachments[0] declares mimeType=undefined, not image/png'],
      ['a Kiro image', kiroImages([{ format: 'pdf', source: { bytes: pngBase64 } }]), 'conversationState.currentMessage.userInputMessage.images[0] declares format=pdf, not image/png'],
      ['a Kiro image with no format', kiroImages([{ source: { bytes: pngBase64 } }]), 'images[0] declares format=undefined, not image/png'],
      ['a Google inlineData part', google({ role: 'user', parts: [{ inlineData: { mimeType: 'image/jpeg', data: pngBase64 } }] }), 'contents[0].parts[0] declares mimeType=image/jpeg, not image/png'],
    ])('rejects %s', async (_name, request, message) => {
      await expect(expectNativeImagePart(request, png, noDecode)).rejects.toThrow(message)
    })

    it('rejects a media type that differs only in case', async () => {
      await expect(expectNativeImagePart(chat({ role: 'user', content: [chatImage(`data:Image/PNG;base64,${pngBase64}`)] }), png, noDecode)).rejects.toThrow('not image/png')
    })

    it('rejects a typed part of another kind that holds the PNG', async () => {
      await expect(expectNativeImagePart(chat({ role: 'user', content: [chatFile(pngDataURI(pngBase64))] }), png, noDecode)).rejects.toThrow(NO_IMAGE_PART)
      await expect(expectNativeImagePart(cursor([{ kind: 'document', data: pngBase64, mimeType: 'image/png' }]), png, noDecode)).rejects.toThrow(NO_IMAGE_PART)
      await expect(expectNativeImagePart(responses({ role: 'user', content: [{ type: 'input_file', file_data: pngDataURI(pngBase64) }] }), png, noDecode)).rejects.toThrow(NO_IMAGE_PART)
    })
  })

  describe('rejects an image part that carries no base64 data URI', () => {
    it.each([
      ['raw base64', pngBase64, 'has a image_url.url that is not of the form data:<type>;base64,<data>'],
      ['a data URI without the base64 marker', `data:image/png,${pngBase64}`, 'has a image_url.url that is not of the form data:<type>;base64,<data>'],
      ['a data URI with a media type parameter', `data:image/png;name=shot.png;base64,${pngBase64}`, 'has a image_url.url that is not of the form data:<type>;base64,<data>'],
      ['a URL', 'https://example.com/shot.png', 'has a image_url.url that is not of the form data:<type>;base64,<data>'],
      ['null', null, 'has null in image_url.url, not a base64 data URI'],
      ['an object', { data: pngBase64 }, 'has an object in image_url.url, not a base64 data URI'],
    ])('rejects %s in a Chat Completions image part', async (_name, url, message) => {
      await expect(expectNativeImagePart(chat({ role: 'user', content: [chatImage(url)] }), png, noDecode)).rejects.toThrow(message)
    })

    it('rejects a Chat Completions image part whose image_url is a bare string or absent', async () => {
      await expect(expectNativeImagePart(chat({ role: 'user', content: [{ type: 'image_url', image_url: pngDataURI(pngBase64) }] }), png, noDecode)).rejects.toThrow('has no value in image_url.url')
      await expect(expectNativeImagePart(chat({ role: 'user', content: [{ type: 'image_url' }] }), png, noDecode)).rejects.toThrow('has no value in image_url.url')
    })

    it('rejects a Responses input image that refers to an uploaded file or wraps its URL in an object', async () => {
      await expect(expectNativeImagePart(responses({ role: 'user', content: [{ type: 'input_image', file_id: 'file-abc' }] }), png, noDecode)).rejects.toThrow('has no value in image_url, not a base64 data URI')
      await expect(expectNativeImagePart(responses({ role: 'user', content: [responsesImage({ url: pngDataURI(pngBase64) })] }), png, noDecode)).rejects.toThrow('has an object in image_url, not a base64 data URI')
    })

    it('rejects an Anthropic image whose source is a URL or absent', async () => {
      await expect(expectNativeImagePart(anthropic({ role: 'user', content: [{ type: 'image', source: { type: 'url', url: 'https://example.com/shot.png' } }] }), png, noDecode))
        .rejects
        .toThrow('messages[0].content[0] has a source of type url, not base64 bytes')
      await expect(expectNativeImagePart(anthropic({ role: 'user', content: [{ type: 'image' }] }), png, noDecode)).rejects.toThrow('messages[0].content[0] has no source')
    })
  })

  describe('rejects empty, null, and non-string data', () => {
    it.each([
      ['an empty string', '', 'has empty data'],
      ['null', null, 'has null in place of base64 bytes'],
      ['an absent value', undefined, 'has no value in place of base64 bytes'],
      ['a number', 0, 'has a number in place of base64 bytes'],
      ['an object', { bytes: pngBase64 }, 'has an object in place of base64 bytes'],
      ['an array', [...png], 'has an array in place of base64 bytes'],
    ])('rejects %s in an Anthropic image', async (_name, data, message) => {
      await expect(expectNativeImagePart(anthropic({ role: 'user', content: [anthropicImage(data)] }), png, noDecode)).rejects.toThrow(message)
    })

    it('rejects a PNG data URI with an empty payload', async () => {
      await expect(expectNativeImagePart(chat({ role: 'user', content: [chatImage(pngDataURI(''))] }), png, noDecode)).rejects.toThrow('has empty data')
    })

    it('rejects a Kiro image with no source and a Cursor image with no data', async () => {
      await expect(expectNativeImagePart(kiroImages([{ format: 'png' }]), png, noDecode)).rejects.toThrow('has no value in place of base64 bytes')
      await expect(expectNativeImagePart(cursor([{ kind: 'image', mimeType: 'image/png' }]), png, noDecode)).rejects.toThrow('has no value in place of base64 bytes')
    })
  })

  describe('rejects bytes that differ from the source', () => {
    it('rejects the PNG signature alone', async () => {
      // The zlib build decides the deflated bytes, so the expected size and hash come from the fixture file.
      const signature = png.subarray(0, 8)
      await expect(expectNativeImagePart(anthropic({ role: 'user', content: [anthropicImage(signature.toString('base64'))] }), png, noDecode))
        .rejects
        .toThrow(`has 8 bytes with SHA-256 ${sha256Hex(signature)}, not the ${png.length}-byte source with SHA-256 ${sha256Hex(png)}`)
    })

    it('rejects the PNG without its last byte', async () => {
      const truncated = png.subarray(0, png.length - 1).toString('base64')
      await expect(expectNativeImagePart(chat({ role: 'user', content: [chatImage(pngDataURI(truncated))] }), png, noDecode)).rejects.toThrow(`has ${png.length - 1} bytes`)
    })

    it('rejects the PNG with one changed byte', async () => {
      const corrupt = Buffer.from(png)
      corrupt[png.length - 2] = corrupt[png.length - 2]! ^ 0x01
      await expect(expectNativeImagePart(kiroImages([{ format: 'png', source: { bytes: corrupt.toString('base64') } }]), png, noDecode)).rejects.toThrow(`has ${png.length} bytes with SHA-256`)
    })

    it('rejects the PNG with extra bytes after it', async () => {
      const extended = Buffer.concat([png, Buffer.from([0])]).toString('base64')
      await expect(expectNativeImagePart(google({ role: 'user', parts: [{ inlineData: { mimeType: 'image/png', data: extended } }] }), png, noDecode)).rejects.toThrow(`has ${png.length + 1} bytes`)
    })

    it('rejects a transcoded image, which a provider that sends the source bytes does not write', async () => {
      await expect(expectNativeImagePart(chat({ role: 'user', content: [chatImage(webpURI)] }), png, noDecode))
        .rejects
        .toThrow('messages[0].content[0] declares image_url.url=data:image/webp, not image/png')
    })

    it.each([
      ['without its padding', () => pngBase64.replace(/=+$/, '')],
      ['in the URL-safe alphabet', () => pngBase64.replaceAll('+', '-').replaceAll('/', '_')],
      ['with a line break every 76 characters', () => pngBase64.replace(/(.{76})/g, '$1\n')],
      ['with surrounding white space', () => ` ${pngBase64} `],
    ])('rejects the complete PNG base64 %s', async (_name, form) => {
      const data = form()
      expect(data).not.toBe(pngBase64)
      expect(Buffer.from(data, 'base64').equals(png)).toBe(true)
      await expect(expectNativeImagePart(anthropic({ role: 'user', content: [anthropicImage(data)] }), png, noDecode)).rejects.toThrow('has data that is not canonical base64')
    })
  })

  describe('rejects the exact PNG outside the current user turn', () => {
    it('rejects a trailing instruction row that carries the PNG', async () => {
      await expect(expectNativeImagePart(anthropic(
        { role: 'user', content: [{ type: 'text', text: 'Inspect the attached file.' }] },
        { role: 'system', content: [anthropicImage(pngBase64)] },
      ), png, noDecode)).rejects.toThrow(NO_IMAGE_PART)
    })

    it('rejects an earlier turn that an assistant row ends, even when an instruction row follows the current user row', async () => {
      await expect(expectNativeImagePart(anthropic(
        { role: 'user', content: [anthropicImage(pngBase64)] },
        { role: 'assistant', content: [{ type: 'text', text: 'Read.' }] },
        { role: 'user', content: [{ type: 'text', text: 'Describe it again.' }] },
        { role: 'system', content: [{ type: 'text', text: '# Environment' }] },
      ), png, noDecode)).rejects.toThrow(NO_IMAGE_PART)
    })

    it('rejects a Responses image after which a tool call output ends the history', async () => {
      await expect(expectNativeImagePart(responses(
        { type: 'message', role: 'user', content: [responsesImage(pngDataURI(pngBase64))] },
        { type: 'function_call', call_id: 'call_1', name: 'shell', arguments: '{}' },
        { type: 'function_call_output', call_id: 'call_1', output: 'done' },
      ), png, noDecode)).rejects.toThrow(NO_IMAGE_PART)
    })

    it('rejects a Cursor image that the mock projects from another route', async () => {
      const elsewhere: MockModelRequestRecord = { ...cursor([{ kind: 'image', data: pngBase64, mimeType: 'image/png' }]), path: '/v1/responses' }
      await expect(expectNativeImagePart(elsewhere, png, noDecode)).rejects.toThrow(NO_IMAGE_PART)
    })
  })

  it('states the location and the defect of each typed part that comes close', async () => {
    const request = chat({ role: 'user', content: [
      { type: 'text', text: pngBase64 },
      chatImage(`data:image/jpeg;base64,${pngBase64}`),
      chatImage(pngDataURI(png.subarray(0, 8).toString('base64'))),
    ] })
    await expect(expectNativeImagePart(request, png, noDecode)).rejects.toThrow(
      'the current user turn of the scripted openai-chat-completions request does not carry the complete image file in a typed image part with the exact source bytes: '
      + 'messages[0].content[1] declares image_url.url=data:image/jpeg, not image/png; '
      + 'messages[0].content[2] has 8 bytes with SHA-256',
    )
  })

  it.each([
    ['anthropic-messages', [undefined, null, 'text', {}, { messages: null }, { messages: [null, {}, { role: 'user', content: null }, { role: 'user', content: [null, 'text'] }] }]],
    ['openai-chat-completions', [undefined, { messages: [{ role: 'user', content: [null, { type: 'image_url', image_url: null }] }] }]],
    ['openai-responses', [undefined, { input: 'text' }, { input: [null, { role: 'user', content: 'text' }] }]],
    ['aws-event-stream', [undefined, { conversationState: null }, { conversationState: { currentMessage: { userInputMessage: { images: null } } } }]],
    ['google-generative-language', [undefined, { contents: null }, { contents: [null, { role: 'user', parts: null }, { role: 'user', parts: [null, { inlineData: null }] }] }]],
  ] satisfies Array<[MockModelProtocol, unknown[]]>)('rejects an absent or malformed %s body', async (protocol, bodies) => {
    for (const body of bodies)
      await expect(expectNativeImagePart(nativeRequest(protocol, body), png, noDecode)).rejects.toThrow(`the current user turn of the scripted ${protocol} request does not carry the complete image file`)
  })

  it('rejects an absent or malformed Cursor projection', async () => {
    for (const attachments of [undefined, null, 'image', [null, { kind: 'file', data: 'text' }]])
      await expect(expectNativeImagePart(cursor(attachments), png, noDecode)).rejects.toThrow(NO_IMAGE_PART)
  })

  it('fails when the fixture has no image signature', async () => {
    await expect(expectNativeImagePart(chat({ role: 'user', content: [chatImage(pngDataURI(pngBase64))] }), Buffer.from('not an image'), noDecode))
      .rejects
      .toThrow('the image fixture has no image signature that the proof can identify')
  })

  describe('accepts a provider that transcodes the image only in the stated format and colors', () => {
    const NO_WEBP_PART = 'does not carry the complete image file in a typed image/webp part that decodes to the four source colors'

    it.each([
      ['a Chat Completions image part', chat({ role: 'user', content: [chatImage(webpURI)] })],
      ['an Anthropic image block', anthropic({ role: 'user', content: [anthropicImage(webpBase64, 'image/webp')] })],
      ['a Responses input image', responses({ role: 'user', content: [responsesImage(webpURI)] })],
      ['a Google inlineData part', google({ role: 'user', parts: [{ inlineData: { mimeType: 'image/webp', data: webpBase64 } }] })],
      ['a Kiro image', kiroImages([{ format: 'webp', source: { bytes: webpBase64 } }])],
    ])('accepts %s whose bytes decode to the four source colors', async (_name, request) => {
      await expect(expectNativeImagePart(request, png, decoderFor(webpURI), 'image/webp')).resolves.toBeUndefined()
    })

    it('rejects the exact PNG, which the provider does not send', async () => {
      await expect(expectNativeImagePart(chat({ role: 'user', content: [chatImage(pngDataURI(pngBase64))] }), png, noDecode, 'image/webp'))
        .rejects
        .toThrow('messages[0].content[0] declares image_url.url=data:image/png, not image/webp')
    })

    it('rejects a part that declares the stated type but holds bytes of another format', async () => {
      await expect(expectNativeImagePart(chat({ role: 'user', content: [chatImage(`data:image/webp;base64,${pngBase64}`)] }), png, noDecode, 'image/webp'))
        .rejects
        .toThrow('messages[0].content[0] holds image/png bytes, not the image/webp that it declares')
      await expect(expectNativeImagePart(chat({ role: 'user', content: [chatImage(`data:image/webp;base64,${Buffer.from('not an image').toString('base64')}`)] }), png, noDecode, 'image/webp'))
        .rejects
        .toThrow('holds bytes with no known image signature, not the image/webp that it declares')
    })

    it('rejects a part that declares another image type, even with bytes of the stated format', async () => {
      await expect(expectNativeImagePart(chat({ role: 'user', content: [chatImage(`data:image/png;base64,${webpBase64}`)] }), png, noDecode, 'image/webp'))
        .rejects
        .toThrow('declares image_url.url=data:image/png, not image/webp')
    })

    it('rejects non-canonical base64 before it decodes the image', async () => {
      const unpadded = webpBase64.replace(/=+$/, '')
      expect(unpadded).not.toBe(webpBase64)
      await expect(expectNativeImagePart(chat({ role: 'user', content: [chatImage(`data:image/webp;base64,${unpadded}`)] }), png, noDecode, 'image/webp'))
        .rejects
        .toThrow('has data that is not canonical base64')
    })

    it.each([
      ['white', [[255, 255, 255, 255], [255, 255, 255, 255], [255, 255, 255, 255], [255, 255, 255, 255]]],
      ['the quadrants in another order', [[0, 255, 0, 255], [255, 0, 0, 255], [0, 0, 255, 255], [255, 255, 0, 255]]],
      ['transparent', [[255, 0, 0, 0], [0, 255, 0, 0], [0, 0, 255, 0], [255, 255, 0, 0]]],
      ['one quadrant past the channel tolerance', [[255, 0, 0, 255], [0, 255, 0, 255], [0, 0, 255, 255], [255, 255, 60, 255]]],
      ['three quadrants', [[255, 0, 0, 255], [0, 255, 0, 255], [0, 0, 255, 255]]],
    ])('rejects a part whose pixels decode to %s', async (_name, pixels) => {
      await expect(expectNativeImagePart(chat({ role: 'user', content: [chatImage(webpURI)] }), png, decoderFor(webpURI, pixels), 'image/webp'))
        .rejects
        .toThrow(`messages[0].content[0] decodes to the quadrant pixels ${JSON.stringify(pixels)}, not the four source colors`)
    })

    it('accepts pixels within the channel tolerance', async () => {
      const near = [[230, 20, 10, 250], [25, 240, 30, 255], [10, 15, 220, 245], [240, 250, 40, 255]]
      await expect(expectNativeImagePart(chat({ role: 'user', content: [chatImage(webpURI)] }), png, decoderFor(webpURI, near), 'image/webp')).resolves.toBeUndefined()
    })

    it('rejects a part that fails to decode', async () => {
      const broken: QuadrantDecoder = async () => {
        throw new Error('the browser cannot decode the image')
      }
      await expect(expectNativeImagePart(chat({ role: 'user', content: [chatImage(webpURI)] }), png, broken, 'image/webp'))
        .rejects
        .toThrow('messages[0].content[0] fails to decode: the browser cannot decode the image')
    })

    it('rejects the transcoded image outside a typed part of the current user turn', async () => {
      const decode = decoderFor(webpURI)
      await expect(expectNativeImagePart(chat({ role: 'user', content: `Inspect this: ${webpURI}` }), png, decode, 'image/webp')).rejects.toThrow(NO_WEBP_PART)
      await expect(expectNativeImagePart(chat(
        { role: 'user', content: [chatImage(webpURI)] },
        { role: 'assistant', content: 'Read it.' },
        { role: 'user', content: 'Inspect the attached file.' },
      ), png, decode, 'image/webp')).rejects.toThrow(NO_WEBP_PART)
      await expect(expectNativeImagePart(anthropic(
        { role: 'user', content: [{ type: 'text', text: 'Inspect the attached file.' }] },
        { role: 'assistant', content: [{ type: 'tool_use', id: 'toolu_1', name: 'Read', input: {} }] },
        { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'toolu_1', content: [anthropicImage(webpBase64, 'image/webp')] }] },
      ), png, decode, 'image/webp')).rejects.toThrow(NO_WEBP_PART)
      await expect(expectNativeImagePart(chat(
        { role: 'user', content: 'Inspect the attached file.' },
        { role: 'system', content: [chatImage(webpURI)] },
      ), png, decode, 'image/webp')).rejects.toThrow(NO_WEBP_PART)
    })
  })
})

describe('expectNativeAttachmentProof', () => {
  const NO_COMPLETE_IMAGE = 'does not carry the complete image file'

  it('accepts the exact PNG in a typed image part without decoding it', async () => {
    await expect(imageProof(anthropic({ role: 'user', content: [{ type: 'text', text: 'Inspect the attached file.' }, anthropicImage(pngBase64)] }))).resolves.toBeUndefined()
  })

  it('applies a transcoded image type to an image attachment only', async () => {
    const request = chat({ role: 'user', content: [chatFile(pdfDataURI(encoded))] })
    await expect(expectNativeAttachmentProof(noBrowser, status(request), 'pdf', pdfPath, { protocol: 'openai-chat-completions', transcodedImageType: 'image/webp' }))
      .rejects
      .toThrow('a transcoded image type applies to an image attachment, not to a pdf attachment')
    await expect(expectNativeAttachmentProof(noBrowser, status(request), 'pdf', pdfPath, { protocol: 'openai-chat-completions' })).resolves.toBeUndefined()
  })

  it('applies a binary media type to a binary attachment only', async () => {
    const request = chat({ role: 'user', content: [chatFile(pdfDataURI(encoded))] })
    await expect(expectNativeAttachmentProof(noBrowser, status(request), 'pdf', pdfPath, { binaryMediaType: 'application/pdf' }))
      .rejects
      .toThrow('a binary media type applies to a binary attachment, not to a pdf attachment')
  })

  it('requires the media type of a binary attachment, so base64 in the user text never proves it', async () => {
    const inText = chat({ role: 'user', content: `Inspect this: ${binary.toString('base64')}` })
    await expect(expectNativeAttachmentProof(noBrowser, status(inText), 'binary', binaryPath))
      .rejects
      .toThrow('a binary attachment proof needs the media type that its typed file part declares')
    await expect(expectNativeAttachmentProof(noBrowser, status(inText), 'binary', binaryPath, { binaryMediaType: /^application\// }))
      .rejects
      .toThrow('carries no typed /^application\\// part with the exact source bytes')
  })

  it('accepts a binary attachment in a typed file part of the declared type', async () => {
    const request = chat({ role: 'user', content: [chatFile(`data:application/macbinary;base64,${binary.toString('base64')}`)] })
    await expect(expectNativeAttachmentProof(noBrowser, status(request), 'binary', binaryPath, { protocol: 'openai-chat-completions', binaryMediaType: /^application\// }))
      .resolves
      .toBeUndefined()
    await expect(expectNativeAttachmentProof(noBrowser, status(request), 'binary', binaryPath, { binaryMediaType: 'application/octet-stream' }))
      .rejects
      .toThrow('declares file.file_data=data:application/macbinary, not application/octet-stream')
  })

  it('reads the request of the step that stepIndex states', async () => {
    const clean = chat({ role: 'user', content: 'Reply once without attachments.' })
    const withPdf = nativeRequest('openai-chat-completions', { messages: [{ role: 'user', content: [chatFile(pdfDataURI(encoded))] }] }, 2)
    await expect(expectNativeAttachmentProof(noBrowser, status(clean, withPdf), 'pdf', pdfPath, { protocol: 'openai-chat-completions', stepIndex: 2 }))
      .resolves
      .toBeUndefined()
    await expect(expectNativeAttachmentProof(noBrowser, status(clean, withPdf), 'pdf', pdfPath, { protocol: 'openai-chat-completions' }))
      .rejects
      .toThrow(NO_PDF_PART)
  })

  it('fails a request on another model API than the protocol states', async () => {
    const request = chat({ role: 'user', content: [chatFile(pdfDataURI(encoded))] })
    await expect(expectNativeAttachmentProof(noBrowser, status(request), 'pdf', pdfPath, { protocol: 'anthropic-messages' })).rejects.toThrow('the scripted request uses the model API of the provider')
  })

  it('decodes a transcoded image in the browser page that it receives', async () => {
    const decoded: string[] = []
    const page = Object.assign({} as Page, {
      evaluate: async (_run: unknown, uri: unknown): Promise<number[][]> => {
        decoded.push(String(uri))
        return SOURCE_QUADRANTS
      },
    })
    await expect(expectNativeAttachmentProof(page, status(chat({ role: 'user', content: [chatImage(webpURI)] })), 'image', pngPath, { protocol: 'openai-chat-completions', transcodedImageType: 'image/webp' }))
      .resolves
      .toBeUndefined()
    expect(decoded).toEqual([webpURI])
  })

  describe('rejects the complete PNG outside a typed image part', () => {
    it.each([
      ['a Chat Completions string', chat({ role: 'user', content: `Inspect this: ${pngBase64}` })],
      ['a Chat Completions text part', chat({ role: 'user', content: [{ type: 'text', text: pngBase64 }] })],
      ['an Anthropic text block', anthropic({ role: 'user', content: [{ type: 'text', text: pngBase64 }] })],
      ['a Responses input text part', responses({ role: 'user', content: [{ type: 'input_text', text: pngBase64 }] })],
      ['a Google text part', google({ role: 'user', parts: [{ text: pngBase64 }] })],
      ['the Kiro user text', nativeRequest('aws-event-stream', { conversationState: { currentMessage: { userInputMessage: { content: pngBase64 } } } })],
    ])('rejects the base64 in %s', async (_name, request) => {
      await expect(imageProof(request)).rejects.toThrow(NO_COMPLETE_IMAGE)
    })

    it('rejects a complete PNG data URI inside a text part', async () => {
      await expect(imageProof(chat({ role: 'user', content: [{ type: 'text', text: pngDataURI(pngBase64) }] }))).rejects.toThrow(NO_COMPLETE_IMAGE)
      await expect(imageProof(anthropic({ role: 'user', content: [{ type: 'text', text: pngDataURI(pngBase64) }] }))).rejects.toThrow(NO_COMPLETE_IMAGE)
    })
  })

  describe('rejects a typed part that declares a type other than the PNG', () => {
    it.each([
      ['a Chat Completions image with a binary type', chat({ role: 'user', content: [chatImage(`data:application/octet-stream;base64,${pngBase64}`)] })],
      ['a Chat Completions image with a text type', chat({ role: 'user', content: [chatImage(`data:text/plain;base64,${pngBase64}`)] })],
      ['an Anthropic image with a binary type', anthropic({ role: 'user', content: [anthropicImage(pngBase64, 'application/octet-stream')] })],
      ['an Anthropic document that holds the PNG', anthropic({ role: 'user', content: [{ type: 'document', source: { type: 'base64', media_type: 'application/pdf', data: pngBase64 } }] })],
      ['a Responses input image with a binary type', responses({ role: 'user', content: [responsesImage(`data:application/octet-stream;base64,${pngBase64}`)] })],
      ['a Google inlineData part with a binary type', google({ role: 'user', parts: [{ inlineData: { mimeType: 'application/octet-stream', data: pngBase64 } }] })],
      ['a Kiro image with a document format', kiroImages([{ format: 'pdf', source: { bytes: pngBase64 } }])],
      ['a Kiro document that holds the PNG', kiro([{ name: 'shot', format: 'pdf', source: { bytes: pngBase64 } }])],
    ])('rejects %s', async (_name, request) => {
      await expect(imageProof(request)).rejects.toThrow(NO_COMPLETE_IMAGE)
    })

    it.each([
      ['a Chat Completions image', chat({ role: 'user', content: [chatImage(`data:image/jpeg;base64,${pngBase64}`)] })],
      ['an Anthropic image', anthropic({ role: 'user', content: [anthropicImage(pngBase64, 'image/webp')] })],
      ['a Google inlineData part', google({ role: 'user', parts: [{ inlineData: { mimeType: 'image/gif', data: pngBase64 } }] })],
      ['a Kiro image', kiroImages([{ format: 'jpeg', source: { bytes: pngBase64 } }])],
    ])('rejects PNG bytes that %s declares as another image type', async (_name, request) => {
      await expect(imageProof(request)).rejects.toThrow(NO_COMPLETE_IMAGE)
    })
  })

  describe('rejects the exact PNG outside the current user turn', () => {
    it('rejects a system message', async () => {
      await expect(imageProof(chat(
        { role: 'system', content: [chatImage(pngDataURI(pngBase64))] },
        { role: 'user', content: 'Inspect the attached file.' },
      ))).rejects.toThrow(NO_COMPLETE_IMAGE)
    })

    it('rejects the Anthropic system field', async () => {
      const request = nativeRequest('anthropic-messages', { system: [anthropicImage(pngBase64)], messages: [{ role: 'user', content: 'Inspect the attached file.' }] })
      await expect(imageProof(request)).rejects.toThrow(NO_COMPLETE_IMAGE)
    })

    it('rejects the Google system instruction', async () => {
      const request = nativeRequest('google-generative-language', {
        systemInstruction: { parts: [{ inlineData: { mimeType: 'image/png', data: pngBase64 } }] },
        contents: [{ role: 'user', parts: [{ text: 'Inspect the attached file.' }] }],
      })
      await expect(imageProof(request)).rejects.toThrow(NO_COMPLETE_IMAGE)
    })

    it('rejects a Responses developer message', async () => {
      await expect(imageProof(responses(
        { role: 'developer', content: [responsesImage(pngDataURI(pngBase64))] },
        { role: 'user', content: [{ type: 'input_text', text: 'Inspect the attached file.' }] },
      ))).rejects.toThrow(NO_COMPLETE_IMAGE)
    })

    it('rejects an assistant message that ends the history', async () => {
      await expect(imageProof(chat(
        { role: 'user', content: 'Inspect the attached file.' },
        { role: 'assistant', content: [chatImage(pngDataURI(pngBase64))] },
      ))).rejects.toThrow(NO_COMPLETE_IMAGE)
    })

    it('rejects a Chat Completions tool message', async () => {
      await expect(imageProof(chat(
        { role: 'user', content: 'Inspect the attached file.' },
        { role: 'assistant', content: null, tool_calls: [{ id: 'call_1', type: 'function', function: { name: 'read', arguments: '{}' } }] },
        { role: 'tool', tool_call_id: 'call_1', content: [chatImage(pngDataURI(pngBase64))] },
      ))).rejects.toThrow(NO_COMPLETE_IMAGE)
    })

    it('rejects an image in the user row that follows a tool row, the synthetic carrier that Copilot and Junie build for a tool\'s image', async () => {
      await expect(imageProof(chat(
        { role: 'user', content: 'Inspect the file.' },
        { role: 'assistant', content: null, tool_calls: [{ id: 'call_1', type: 'function', function: { name: 'view', arguments: '{}' } }] },
        { role: 'tool', tool_call_id: 'call_1', content: 'viewed' },
        { role: 'user', content: [chatImage(pngDataURI(pngBase64))] },
      ))).rejects.toThrow(NO_COMPLETE_IMAGE)
    })

    it('rejects the same synthetic carrier when a genuine user row follows it in the same turn', async () => {
      await expect(imageProof(chat(
        { role: 'user', content: 'Inspect the file.' },
        { role: 'assistant', content: null, tool_calls: [{ id: 'call_1', type: 'function', function: { name: 'view', arguments: '{}' } }] },
        { role: 'tool', tool_call_id: 'call_1', content: 'viewed' },
        { role: 'user', content: [chatImage(pngDataURI(pngBase64))] },
        { role: 'user', content: 'What did you see?' },
      ))).rejects.toThrow(NO_COMPLETE_IMAGE)
    })

    it('accepts the part in a user row that follows the assistant row that answered a tool call', async () => {
      await expect(imageProof(chat(
        { role: 'user', content: 'Inspect the file.' },
        { role: 'assistant', content: null, tool_calls: [{ id: 'call_1', type: 'function', function: { name: 'view', arguments: '{}' } }] },
        { role: 'tool', tool_call_id: 'call_1', content: 'viewed' },
        { role: 'assistant', content: 'Viewed.' },
        { role: 'user', content: [chatImage(pngDataURI(pngBase64))] },
      ))).resolves.toBeUndefined()
    })

    it('rejects an image inside an Anthropic tool result, which a file read produces', async () => {
      await expect(imageProof(anthropic(
        { role: 'user', content: [{ type: 'text', text: 'Inspect the attached file.' }] },
        { role: 'assistant', content: [{ type: 'tool_use', id: 'toolu_1', name: 'Read', input: {} }] },
        { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'toolu_1', content: [anthropicImage(pngBase64)] }] },
      ))).rejects.toThrow(NO_COMPLETE_IMAGE)
    })

    it('rejects an Anthropic image beside a tool result', async () => {
      await expect(imageProof(anthropic(
        { role: 'user', content: [{ type: 'text', text: 'Inspect the attached file.' }] },
        { role: 'assistant', content: [{ type: 'tool_use', id: 'toolu_1', name: 'Read', input: {} }] },
        { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'toolu_1', content: 'Read the image.' }, anthropicImage(pngBase64)] },
      ))).rejects.toThrow(NO_COMPLETE_IMAGE)
    })

    it('rejects a Google inlineData part beside a function response', async () => {
      await expect(imageProof(google(
        { role: 'user', parts: [{ text: 'Inspect the attached file.' }] },
        { role: 'model', parts: [{ functionCall: { name: 'read_file', args: {} } }] },
        { role: 'user', parts: [{ functionResponse: { name: 'read_file', response: { output: 'Read the image.' } } }, { inlineData: { mimeType: 'image/png', data: pngBase64 } }] },
      ))).rejects.toThrow(NO_COMPLETE_IMAGE)
    })

    it('rejects a Kiro image of a tool result turn', async () => {
      const request = nativeRequest('aws-event-stream', { conversationState: { currentMessage: { userInputMessage: {
        content: '',
        images: [{ format: 'png', source: { bytes: pngBase64 } }],
        userInputMessageContext: { toolResults: [{ toolUseId: 'tool_1', status: 'success', content: [{ text: 'Read the image.' }] }] },
      } } } })
      await expect(imageProof(request)).rejects.toThrow(NO_COMPLETE_IMAGE)
    })

    it('rejects a Chat Completions image that only an earlier turn carries', async () => {
      await expect(imageProof(chat(
        { role: 'user', content: [chatImage(pngDataURI(pngBase64))] },
        { role: 'assistant', content: 'Read it.' },
        { role: 'user', content: 'Inspect the attached file.' },
      ))).rejects.toThrow(NO_COMPLETE_IMAGE)
    })

    it('rejects an Anthropic image that only an earlier turn carries', async () => {
      await expect(imageProof(anthropic(
        { role: 'user', content: [anthropicImage(pngBase64)] },
        { role: 'assistant', content: [{ type: 'text', text: 'Read it.' }] },
        { role: 'user', content: [{ type: 'text', text: 'Inspect the attached file.' }] },
      ))).rejects.toThrow(NO_COMPLETE_IMAGE)
    })

    it('rejects a Responses input image that only an earlier turn carries', async () => {
      await expect(imageProof(responses(
        { type: 'message', role: 'user', content: [responsesImage(pngDataURI(pngBase64))] },
        { type: 'message', role: 'assistant', content: [{ type: 'output_text', text: 'Read it.' }] },
        { type: 'message', role: 'user', content: [{ type: 'input_text', text: 'Inspect the attached file.' }] },
      ))).rejects.toThrow(NO_COMPLETE_IMAGE)
    })

    it('rejects a Google inlineData image that only an earlier turn carries', async () => {
      await expect(imageProof(google(
        { role: 'user', parts: [{ inlineData: { mimeType: 'image/png', data: pngBase64 } }] },
        { role: 'model', parts: [{ text: 'Read it.' }] },
        { role: 'user', parts: [{ text: 'Inspect the attached file.' }] },
      ))).rejects.toThrow(NO_COMPLETE_IMAGE)
    })

    it('rejects a Kiro image that only the history carries', async () => {
      const earlier = { userInputMessage: { content: 'Earlier.', images: [{ format: 'png', source: { bytes: pngBase64 } }] } }
      await expect(imageProof(kiroImages([], [earlier]))).rejects.toThrow(NO_COMPLETE_IMAGE)
    })
  })
})

/** A context whose script records each queued step, so a refused option can show that no turn started. */
function scriptedContext(textStep?: NativeScenarioContext['textStep']) {
  const queue = vi.fn<(...steps: unknown[]) => Promise<number>>(async () => 0)
  const modelScript = { queue } as unknown as ModelScript
  const context: NativeScenarioContext = { page: noBrowser, modelScript, provider: AgentProvider.CLAUDE_CODE, ...(textStep ? { textStep } : {}) }
  return { context, queue }
}

describe('exerciseAttachmentDelivery', () => {
  it.each<[string, AttachmentKind, AttachmentDeliveryOptions, string]>([
    ['a custom proof with a transcoded image type', 'image', { proof: () => undefined, transcodedImageType: 'image/webp' }, 'a custom attachment proof replaces the typed-part proof'],
    ['a custom proof with a binary media type', 'binary', { proof: () => undefined, binaryMediaType: 'audio/wav' }, 'a custom attachment proof replaces the typed-part proof'],
    ['a transcoded image type for a PDF', 'pdf', { transcodedImageType: 'image/png' }, 'a transcoded image type applies to an image attachment, not to a pdf attachment'],
    ['a binary media type for a text file', 'text', { binaryMediaType: 'text/plain' }, 'a binary media type applies to a binary attachment, not to a text attachment'],
    ['a binary file with no media type and no proof', 'binary', {}, 'a binary attachment proof needs the media type that its typed file part declares'],
  ])('refuses %s before it queues a step', async (_name, kind, options, message) => {
    const { context, queue } = scriptedContext()
    await expect(exerciseAttachmentDelivery(context, kind, 'attachment.bin', options)).rejects.toThrow(message)
    expect(queue).not.toHaveBeenCalled()
  })

  it('accepts a custom proof for a binary file with no media type', async () => {
    const stop = new Error('The fake script stops the turn.')
    const { context, queue } = scriptedContext()
    queue.mockRejectedValueOnce(stop)
    await expect(exerciseAttachmentDelivery(context, 'binary', 'grok-blob.bin', { proof: () => undefined })).rejects.toBe(stop)
    expect(queue).toHaveBeenCalledOnce()
  })

  it('queues its answer through the text step of the context', async () => {
    const stop = new Error('The fake script stops the turn.')
    const answer = (text: string) => ({ toolCalls: [{ id: 'native-answer', name: 'answer', arguments: { text } }] })
    const { context, queue } = scriptedContext(answer)
    queue.mockRejectedValueOnce(stop)
    await expect(exerciseAttachmentDelivery(context, 'text', 'notes.txt')).rejects.toBe(stop)
    expect(queue).toHaveBeenCalledExactlyOnceWith(answer('Attachment received.'))
  })
})

describe('expectRefusedAttachmentsAbsent', () => {
  it('refuses an empty list of refused files before it queues a step', async () => {
    const { context, queue } = scriptedContext()
    await expect(expectRefusedAttachmentsAbsent(context, [])).rejects.toThrow('a refusal proof needs at least one refused file')
    expect(queue).not.toHaveBeenCalled()
  })

  it('queues its clean answer through the text step of the context', async () => {
    const stop = new Error('The fake script stops the turn.')
    const answer = (text: string) => ({ toolCalls: [{ id: 'native-answer', name: 'answer', arguments: { text } }] })
    const { context, queue } = scriptedContext(answer)
    queue.mockRejectedValueOnce(stop)
    await expect(expectRefusedAttachmentsAbsent(context, [binaryPath])).rejects.toBe(stop)
    expect(queue).toHaveBeenCalledExactlyOnceWith(answer('The clean prompt answered.'))
  })
})

describe('scriptedRequest', () => {
  it('selects the request that consumed the given step', () => {
    const withPdf = chat({ role: 'user', content: [chatFile(pdfDataURI(encoded))] })
    const clean = nativeRequest('openai-chat-completions', { messages: [{ role: 'user', content: 'Reply once without attachments.' }] }, 1)
    const recorded = status(withPdf, clean)
    expect(scriptedRequest(recorded)).toBe(withPdf)
    expect(scriptedRequest(recorded, 'openai-chat-completions', 1)).toBe(clean)
    expect(() => expectNativePdfPart(scriptedRequest(recorded), pdf)).not.toThrow()
    expect(() => expectNativePdfPart(scriptedRequest(recorded, undefined, 1), pdf)).toThrow(NO_PDF_PART)
  })

  it('skips a request that a rule answered without a step', () => {
    const ruled: MockModelRequestRecord = { protocol: 'openai-chat-completions', path: '/', rule: 'title', body: {} }
    expect(() => scriptedRequest(status(ruled))).toThrow('The model script holds no request for step 0')
  })

  it('fails when no request consumed the step', () => {
    expect(() => scriptedRequest(status(), undefined, 0)).toThrow('The model script holds no request for step 0: the agent did not request it')
    expect(() => scriptedRequest(status(chat()), undefined, 1)).toThrow('The model script holds no request for step 1: the agent did not request it')
  })

  it('fails when the request uses another protocol than the caller states', () => {
    expect(() => scriptedRequest(status(chat()), 'anthropic-messages')).toThrow('the scripted request uses the model API of the provider')
  })
})
