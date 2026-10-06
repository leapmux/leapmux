import type { Page } from '@playwright/test'
import type { AttachmentKind } from './attachments'
import type { MockModelProtocol, MockModelRequestRecord, MockModelScenarioStatus, MockModelStep } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import { Buffer } from 'node:buffer'
import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { basename } from 'node:path'
import { expect } from '@playwright/test'
import { AMP_ACTOR_PATH_PREFIX } from './ampSurface'
import { expectAttachmentOutcome, PDF_PAGE_MARKER, sendWithAttachment } from './attachments'
import { CURSOR_RUN_PATH } from './cursorSurface'
import { stepRequest } from './mockModelScript'
import { assistantBubbles, expectUserMessage, sendMessage, waitForAgentIdle } from './ui'

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
export function nativeUserStrings(body: unknown): string[] {
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
  if (Array.isArray(request.contents)) {
    for (const row of request.contents) {
      const message = record(row)
      if (message?.role !== 'user' || !Array.isArray(message.parts))
        continue
      for (const value of message.parts) {
        const part = record(value)
        if (part && (typeof part.text === 'string' || record(part.inlineData)))
          parts.push(part)
      }
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

/** Select the native model request that consumed one ordered step of the script. */
export function scriptedRequest(status: MockModelScenarioStatus, protocol?: MockModelProtocol, stepIndex = 0): MockModelRequestRecord {
  const request = stepRequest(status, stepIndex)
  if (protocol)
    expect(request.protocol).toBe(protocol)
  return request
}

/**
 * One part that a native model request declares as a typed file part.
 *
 * A reader for each protocol selects these parts by their shape alone. The
 * proof then checks the declared type and the bytes, so that a failure can say
 * which part came close and why it does not prove the handoff. A part whose
 * shape carries no payload to decode holds only its defect.
 */
type TypedFilePart = { location: string } & (
  | {
    /** The type field, as the protocol spells it. For example, `format=pdf`. */
    declaredType: string
    /** The media type that the type field states, or undefined when it states none. */
    mediaType: string | undefined
    /** The base64 payload. The request can carry any value here. */
    data: unknown
  }
  | { defect: string }
)

const PDF_MEDIA_TYPE = 'application/pdf'

/** An image media type whose encoded bytes the image proof can identify by their signature. */
export type ImageMediaType = 'image/png' | 'image/jpeg' | 'image/gif' | 'image/webp'

/** Kiro states a file type as a short format word. This maps each word that a proof reads to its media type. */
const KIRO_FORMAT_MEDIA_TYPES: ReadonlyMap<unknown, string> = new Map([
  ['pdf', PDF_MEDIA_TYPE],
  ['png', 'image/png'],
  ['jpeg', 'image/jpeg'],
  ['gif', 'image/gif'],
  ['webp', 'image/webp'],
])

/** Return a declared media type, or undefined when the field holds no string. */
function mediaTypeOf(value: unknown): string | undefined {
  return typeof value === 'string' ? value : undefined
}

/** Roles that carry instructions, not a turn. A native agent can place such a row after the user rows. */
const INSTRUCTION_ROLES: ReadonlySet<unknown> = new Set(['system', 'developer'])

/**
 * Return the rows of the current turn: the user rows at the end of the history.
 *
 * An earlier turn ends with a model, assistant, or tool row. So a file that only an earlier user
 * row carries stays out, and so does a file in a system, model, or tool row.
 * An instruction row is skipped, not taken as a turn boundary: Claude Code 2.1 sends its
 * environment as a system row after the current user row.
 * The row of the turn that directly follows a `tool` row is a tool's own output, not user input:
 * Chat Completions agents such as Copilot and Junie return a tool's file there as a synthetic
 * user row, so that row stays out too, while a user row after the assistant row that answered the
 * tool call is input and stays in.
 */
function currentUserTurn(rows: unknown): Array<[number, Record<string, unknown>]> {
  if (!Array.isArray(rows))
    return []
  const turn: Array<[number, Record<string, unknown>]> = []
  for (let index = rows.length - 1; index >= 0; index--) {
    const row = record(rows[index])
    if (INSTRUCTION_ROLES.has(row?.role))
      continue
    if (row?.role !== 'user') {
      const firstInTurn = turn[0]
      if (row?.role === 'tool' && firstInTurn && firstInTurn[0] === index + 1)
        turn.shift()
      break
    }
    turn.unshift([index, row])
  }
  return turn
}

/** Describe the kind of a value that stands where a string must be. */
function describeValue(value: unknown): string {
  if (value === undefined)
    return 'no value'
  if (value === null)
    return 'null'
  if (Array.isArray(value))
    return 'an array'
  return typeof value === 'object' ? 'an object' : `a ${typeof value}`
}

const BASE64_DATA_URI = /^data:([^;,]+);base64,(.*)$/s

/**
 * Read a typed file part whose payload is a base64 data URI.
 *
 * Chat Completions carries a PDF as `file.file_data` and an image as
 * `image_url.url`. OpenAI Responses carries an image as `image_url`. Each must
 * have the form `data:<type>;base64,<data>`. Each provider that a proof covers
 * writes that form with no media type parameter. A raw base64 string, a URL, a
 * data URI without `;base64`, or a data URI with a parameter is a defect.
 */
function dataURIPart(location: string, field: string, value: unknown): TypedFilePart {
  if (typeof value !== 'string')
    return { location, defect: `has ${describeValue(value)} in ${field}, not a base64 data URI` }
  const match = BASE64_DATA_URI.exec(value)
  if (!match)
    return { location, defect: `has a ${field} that is not of the form data:<type>;base64,<data>` }
  const mime = match[1] ?? ''
  return { location, declaredType: `${field}=data:${mime}`, mediaType: mime, data: match[2] ?? '' }
}

/** Read the source of an Anthropic content block. Only a base64 source carries the bytes. */
function anthropicSourcePart(location: string, value: unknown): TypedFilePart {
  const source = record(value)
  if (!source)
    return { location, defect: 'has no source' }
  if (source.type !== 'base64')
    return { location, defect: `has a source of type ${String(source.type)}, not base64 bytes` }
  return { location, declaredType: `media_type=${String(source.media_type)}`, mediaType: mediaTypeOf(source.media_type), data: source.data }
}

/** Return the typed part that one content element declares, or undefined for an element of another kind. */
type ContentReader = (element: Record<string, unknown>, location: string) => TypedFilePart | undefined

/** Return true for the content of a user row that answers a tool call instead of holding the user's input. */
type ToolResultRow = (content: unknown[]) => boolean

/**
 * Read the typed parts of the current user turn, one content element at a time.
 *
 * `rowsField` and `contentField` state the protocol's field names, which also
 * give each part its location. A row for which `answersToolCall` is true holds
 * a tool's output, so its parts stay out.
 */
function currentTurnContentParts(
  body: unknown,
  rowsField: string,
  contentField: string,
  read: ContentReader,
  answersToolCall: ToolResultRow = () => false,
): TypedFilePart[] {
  const parts: TypedFilePart[] = []
  for (const [index, row] of currentUserTurn(record(body)?.[rowsField])) {
    const content = row[contentField]
    if (!Array.isArray(content) || answersToolCall(content))
      continue
    content.forEach((value, position) => {
      const element = record(value)
      const part = element ? read(element, `${rowsField}[${index}].${contentField}[${position}]`) : undefined
      if (part)
        parts.push(part)
    })
  }
  return parts
}

/** Anthropic Messages: a `document` block with a base64 source in a user message that answers no tool call. */
function anthropicFileParts(body: unknown): TypedFilePart[] {
  return currentTurnContentParts(body, 'messages', 'content', (block, location) =>
    block.type === 'document' ? anthropicSourcePart(location, block.source) : undefined, holdsAnthropicToolResult)
}

/** Chat Completions: a `file` part with `file.file_data` in a user message. */
function chatCompletionFileParts(body: unknown): TypedFilePart[] {
  return currentTurnContentParts(body, 'messages', 'content', (part, location) =>
    part.type === 'file' ? dataURIPart(location, 'file.file_data', record(part.file)?.file_data) : undefined)
}

/** Return the user input message of Kiro's current turn. */
function kiroCurrentUserInput(body: unknown): Record<string, unknown> | null {
  const state = record(record(body)?.conversationState)
  return record(record(state?.currentMessage)?.userInputMessage)
}

/** Read the entries of one file list of Kiro's current user input message. */
function kiroFileParts(user: Record<string, unknown> | null, field: 'documents' | 'images'): TypedFilePart[] {
  const entries = user?.[field]
  if (!Array.isArray(entries))
    return []
  return entries.map((value, position) => {
    const entry = record(value)
    return {
      location: `conversationState.currentMessage.userInputMessage.${field}[${position}]`,
      declaredType: `format=${String(entry?.format)}`,
      mediaType: KIRO_FORMAT_MEDIA_TYPES.get(entry?.format),
      data: record(entry?.source)?.bytes,
    }
  })
}

/**
 * Kiro's service request: a document of the current user input message.
 *
 * Kiro states the current turn as `currentMessage` and keeps every earlier turn
 * in `history`, so a document in `history` stays out.
 */
function kiroDocumentParts(body: unknown): TypedFilePart[] {
  return kiroFileParts(kiroCurrentUserInput(body), 'documents')
}

/** Read a Google `inlineData` part. */
function googleInlinePart(part: Record<string, unknown>, location: string): TypedFilePart | undefined {
  const inline = record(part.inlineData)
  if (!inline)
    return undefined
  return { location, declaredType: `mimeType=${String(inline.mimeType)}`, mediaType: mediaTypeOf(inline.mimeType), data: inline.data }
}

/** Google Generative Language: an `inlineData` part in a user content that answers no function call. */
function googleInlineParts(body: unknown): TypedFilePart[] {
  return currentTurnContentParts(body, 'contents', 'parts', googleInlinePart, holdsGoogleFunctionResponse)
}

/** Select the typed file parts of the current user turn, by the request's own protocol. */
function typedFileParts(protocol: MockModelProtocol, body: unknown): TypedFilePart[] {
  switch (protocol) {
    case 'anthropic-messages':
      return anthropicFileParts(body)
    case 'openai-chat-completions':
      return chatCompletionFileParts(body)
    case 'aws-event-stream':
      return kiroDocumentParts(body)
    case 'google-generative-language':
      return googleInlineParts(body)
    case 'openai-responses':
      throw new Error('no provider that accepts a PDF uses the openai-responses protocol, so the PDF proof has no reader for it')
  }
}

function sha256(bytes: Buffer): string {
  return createHash('sha256').update(bytes).digest('hex')
}

/** Decode the payload of a typed part, or state why it is not canonical base64 bytes. */
function decodeCanonicalBase64(data: unknown): { bytes: Buffer } | { defect: string } {
  if (typeof data !== 'string')
    return { defect: `has ${describeValue(data)} in place of base64 bytes` }
  if (data === '')
    return { defect: 'has empty data' }
  // Buffer.from skips characters outside the alphabet and accepts absent padding
  // and the URL-safe alphabet. Only canonical base64 encodes back to itself.
  const bytes = Buffer.from(data, 'base64')
  if (bytes.toString('base64') !== data)
    return { defect: 'has data that is not canonical base64' }
  return { bytes }
}

/** Return why decoded bytes differ from the source file, or undefined when they are equal. */
function sourceBytesDefect(bytes: Buffer, source: Buffer): string | undefined {
  if (bytes.equals(source))
    return undefined
  return `has ${bytes.length} bytes with SHA-256 ${sha256(bytes)}, not the ${source.length}-byte source with SHA-256 ${sha256(source)}`
}

/** Return why a typed part does not carry the exact source PDF, or undefined when it does. */
function pdfPartDefect(part: TypedFilePart, source: Buffer): string | undefined {
  if ('defect' in part)
    return part.defect
  if (part.mediaType !== PDF_MEDIA_TYPE)
    return `declares ${part.declaredType}, not PDF`
  const decoded = decodeCanonicalBase64(part.data)
  if ('defect' in decoded)
    return decoded.defect
  return sourceBytesDefect(decoded.bytes, source)
}

/**
 * Require the exact source PDF in a typed PDF part of the request's current user turn.
 *
 * The native agent sends this request, so the part proves both stages of the
 * handoff: the agent received the original bytes, and it gave them to the model
 * as a PDF. Page text or a page image proves neither stage, because another file
 * can produce the same sample. Base64 inside text does not give the model a PDF.
 */
export function expectNativePdfPart(request: MockModelRequestRecord, source: Buffer): void {
  const defects: string[] = []
  for (const part of typedFileParts(request.protocol, request.body)) {
    const defect = pdfPartDefect(part, source)
    if (defect === undefined)
      return
    defects.push(`${part.location} ${defect}`)
  }
  const detail = defects.length > 0 ? `: ${defects.join('; ')}` : ''
  throw new Error(`the current user turn of the scripted ${request.protocol} request carries no typed PDF part with the exact source bytes${detail}`)
}

const PNG_SIGNATURE = Buffer.from([0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A])

/** Identify the image format of encoded bytes by the signature at their start. */
function encodedImageType(bytes: Buffer): ImageMediaType | undefined {
  if (bytes.subarray(0, PNG_SIGNATURE.length).equals(PNG_SIGNATURE))
    return 'image/png'
  if (bytes.length >= 3 && bytes[0] === 0xFF && bytes[1] === 0xD8 && bytes[2] === 0xFF)
    return 'image/jpeg'
  const head = bytes.toString('latin1', 0, 12)
  if (head.startsWith('GIF87a') || head.startsWith('GIF89a'))
    return 'image/gif'
  if (head.startsWith('RIFF') && head.slice(8, 12) === 'WEBP')
    return 'image/webp'
  return undefined
}

/** An Anthropic user message that holds a `tool_result` block answers a tool call. */
function holdsAnthropicToolResult(content: unknown[]): boolean {
  return content.some(block => record(block)?.type === 'tool_result')
}

/** A Google user content that holds a `functionResponse` part answers a function call. */
function holdsGoogleFunctionResponse(content: unknown[]): boolean {
  return content.some(part => record(record(part)?.functionResponse) !== null)
}

/**
 * Anthropic Messages: a top-level `image` block with a base64 source in a user message.
 *
 * A tool result can carry an image inside its own content, or beside it in the
 * same message. Both are a tool's output, so a message that holds a tool result
 * stays out.
 */
function anthropicImageParts(body: unknown): TypedFilePart[] {
  return currentTurnContentParts(body, 'messages', 'content', (block, location) =>
    block.type === 'image' ? anthropicSourcePart(location, block.source) : undefined, holdsAnthropicToolResult)
}

/**
 * Amp's thread message: an `image` block in Amp's own shape in a user message.
 *
 * The mock records the content that Amp appends to its thread, unchanged. Amp's block
 * spells the media type `mediaType` and adds `sourcePath`, unlike an Anthropic block
 * (see `providers/amp/attachments.go` in the backend). A message that holds a tool result stays out.
 */
function ampImageParts(body: unknown): TypedFilePart[] {
  return currentTurnContentParts(body, 'messages', 'content', (block, location) => {
    if (block.type !== 'image')
      return undefined
    const source = record(block.source)
    if (!source)
      return { location, defect: 'has no source' }
    if (source.type !== 'base64')
      return { location, defect: `has a source of type ${String(source.type)}, not base64 bytes` }
    return { location, declaredType: `mediaType=${String(source.mediaType)}`, mediaType: mediaTypeOf(source.mediaType), data: source.data }
  }, holdsAnthropicToolResult)
}

/** Chat Completions: an `image_url` part with a base64 data URI in a user message. A tool result is a `tool` row, which ends the turn. */
function chatCompletionImageParts(body: unknown): TypedFilePart[] {
  return currentTurnContentParts(body, 'messages', 'content', (part, location) =>
    part.type === 'image_url' ? dataURIPart(location, 'image_url.url', record(part.image_url)?.url) : undefined)
}

/** OpenAI Responses: an `input_image` part in a user message. A tool result is a `function_call_output` item, which ends the turn. */
function responsesImageParts(body: unknown): TypedFilePart[] {
  return currentTurnContentParts(body, 'input', 'content', (part, location) =>
    part.type === 'input_image' ? dataURIPart(location, 'image_url', part.image_url) : undefined)
}

/**
 * Cursor's Run request: an image of the user's selected context.
 *
 * The mock keeps Cursor's protobuf request and records a projection of it
 * (see `cursorAttachmentPayloads`). Each entry of kind `image` comes from the
 * image field of the context that Cursor sends with the current prompt, with
 * its raw bytes in base64 and the media type field that Cursor declares.
 */
function cursorImageParts(body: unknown): TypedFilePart[] {
  const attachments = record(body)?.attachments
  if (!Array.isArray(attachments))
    return []
  const parts: TypedFilePart[] = []
  attachments.forEach((value, position) => {
    const attachment = record(value)
    if (attachment?.kind === 'image')
      parts.push({ location: `attachments[${position}]`, declaredType: `mimeType=${String(attachment.mimeType)}`, mediaType: mediaTypeOf(attachment.mimeType), data: attachment.data })
  })
  return parts
}

/**
 * Kiro's service request: an image of the current user input message.
 *
 * Kiro returns a tool's image in the `images` of the message that carries the
 * tool results, so the images of such a message stay out.
 */
function kiroImageParts(body: unknown): TypedFilePart[] {
  const user = kiroCurrentUserInput(body)
  const toolResults = record(user?.userInputMessageContext)?.toolResults
  if (Array.isArray(toolResults) && toolResults.length > 0)
    return []
  return kiroFileParts(user, 'images')
}

/** Google Generative Language: an `inlineData` part in a user content that answers no function call. */
function googleImageParts(body: unknown): TypedFilePart[] {
  return currentTurnContentParts(body, 'contents', 'parts', googleInlinePart, holdsGoogleFunctionResponse)
}

/** Select the typed image parts of the current user turn, by the request's own protocol and route. */
function typedImageParts(request: MockModelRequestRecord): TypedFilePart[] {
  switch (request.protocol) {
    case 'anthropic-messages':
      return request.path === AMP_ACTOR_PATH_PREFIX ? ampImageParts(request.body) : anthropicImageParts(request.body)
    case 'openai-chat-completions':
      return chatCompletionImageParts(request.body)
    case 'openai-responses':
      return request.path === CURSOR_RUN_PATH ? cursorImageParts(request.body) : responsesImageParts(request.body)
    case 'aws-event-stream':
      return kiroImageParts(request.body)
    case 'google-generative-language':
      return googleImageParts(request.body)
  }
}

/** Decode an image data URI and return the RGBA pixel at the center of each quadrant, in reading order. */
export type QuadrantDecoder = (dataURI: string) => Promise<number[][]>

/** Return why a typed part does not carry the exact source image, or undefined when it does. */
function exactImagePartDefect(part: TypedFilePart, source: Buffer, sourceType: ImageMediaType): string | undefined {
  if ('defect' in part)
    return part.defect
  if (part.mediaType !== sourceType)
    return `declares ${part.declaredType}, not ${sourceType}`
  const decoded = decodeCanonicalBase64(part.data)
  if ('defect' in decoded)
    return decoded.defect
  return sourceBytesDefect(decoded.bytes, source)
}

/** Return why a typed part does not carry the source image transcoded into `mediaType`, or undefined when it does. */
async function transcodedImagePartDefect(part: TypedFilePart, mediaType: ImageMediaType, decodeQuadrants: QuadrantDecoder): Promise<string | undefined> {
  if ('defect' in part)
    return part.defect
  if (part.mediaType !== mediaType)
    return `declares ${part.declaredType}, not ${mediaType}`
  const decoded = decodeCanonicalBase64(part.data)
  if ('defect' in decoded)
    return decoded.defect
  const encodedType = encodedImageType(decoded.bytes)
  if (encodedType !== mediaType)
    return `holds ${encodedType === undefined ? 'bytes with no known image signature' : `${encodedType} bytes`}, not the ${mediaType} that it declares`
  let pixels: number[][]
  try {
    pixels = await decodeQuadrants(`data:${mediaType};base64,${part.data}`)
  }
  catch (error) {
    return `fails to decode: ${error instanceof Error ? error.message : String(error)}`
  }
  if (!hasQuadrantColors(pixels))
    return `decodes to the quadrant pixels ${JSON.stringify(pixels)}, not the four source colors`
  return undefined
}

/**
 * Require the source image in a typed image part of the request's current user turn.
 *
 * By default the part must declare the source's own image type and carry the
 * exact source bytes in canonical base64. The native agent sends this request,
 * so the part proves both stages of the handoff: the agent received the
 * original bytes, and it gave them to the model as an image.
 *
 * Some providers re-encode an image before the request. For such a provider,
 * `transcodedImageType` states the media type that it writes. The part must then
 * declare that type, hold bytes of that format, and decode to the four quadrant
 * colors of the fixture. Set it only from evidence that the provider transcodes.
 *
 * Base64 or a data URI inside text never counts, because it does not give the
 * model an image. Neither does an image in an instruction row, a model row, an
 * earlier turn, or a tool result.
 */
export async function expectNativeImagePart(
  request: MockModelRequestRecord,
  source: Buffer,
  decodeQuadrants: QuadrantDecoder,
  transcodedImageType?: ImageMediaType,
): Promise<void> {
  const sourceType = encodedImageType(source)
  if (sourceType === undefined)
    throw new Error('the image fixture has no image signature that the proof can identify')
  const defects: string[] = []
  for (const part of typedImageParts(request)) {
    const defect = transcodedImageType === undefined
      ? exactImagePartDefect(part, source, sourceType)
      : await transcodedImagePartDefect(part, transcodedImageType, decodeQuadrants)
    if (defect === undefined)
      return
    defects.push(`${part.location} ${defect}`)
  }
  const form = transcodedImageType === undefined
    ? 'a typed image part with the exact source bytes'
    : `a typed ${transcodedImageType} part that decodes to the four source colors`
  const detail = defects.length > 0 ? `: ${defects.join('; ')}` : ''
  throw new Error(`the current user turn of the scripted ${request.protocol} request does not carry the complete image file in ${form}${detail}`)
}

/** Find image payloads anywhere in the given strings, for the check that a refused image stays out. */
function imageDataURIs(strings: string[]): string[] {
  const urls = new Set<string>()
  for (const value of strings) {
    for (const match of value.matchAll(/data:image\/(?:png|webp|jpeg);base64,[a-z0-9+/=]+/gi))
      urls.add(match[0])
    if (!/^[a-z0-9+/=]{80,}$/i.test(value))
      continue
    const encodedType = encodedImageType(Buffer.from(value, 'base64'))
    if (encodedType === 'image/png' || encodedType === 'image/webp')
      urls.add(`data:${encodedType};base64,${value}`)
  }
  return [...urls]
}

async function imageQuadrants(page: Pick<Page, 'evaluate'>, dataURI: string): Promise<number[][]> {
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

/** How a provider hands an image attachment to its model. */
export interface ImageHandoff {
  /**
   * The media type that the provider re-encodes the image into before its model
   * request. Leave it absent for a provider that sends the source bytes
   * unchanged. Set it only from evidence that the provider transcodes, because
   * it replaces the exact-byte proof with a decoded-color proof.
   */
  transcodedImageType?: ImageMediaType
}

/**
 * Prove the source file's content in the scripted native user request.
 *
 * The request of the step at `stepIndex` carries the proof: the first ordered
 * step by default, a later one for a test that holds a turn before the
 * attachment turn. A PDF needs a typed PDF part with the exact source bytes
 * (see `expectNativePdfPart`). An image needs a typed image part of the current
 * user turn (see `expectNativeImagePart`).
 */
export async function expectNativeAttachmentProof(
  page: Page,
  status: MockModelScenarioStatus,
  kind: AttachmentKind,
  sourcePath: string,
  protocol?: MockModelProtocol,
  handoff: ImageHandoff = {},
  stepIndex = 0,
): Promise<void> {
  await expectAttachmentInRequest(page, scriptedRequest(status, protocol, stepIndex), kind, sourcePath, handoff)
}

async function expectAttachmentInRequest(page: Page, request: MockModelRequestRecord, kind: AttachmentKind, sourcePath: string, handoff: ImageHandoff): Promise<void> {
  if (handoff.transcodedImageType !== undefined && kind !== 'image')
    throw new Error(`a transcoded image type applies to an image attachment, not to a ${kind} attachment`)
  const source = readFileSync(sourcePath)
  if (kind === 'pdf') {
    expectNativePdfPart(request, source)
    return
  }
  if (kind === 'image') {
    await expectNativeImagePart(request, source, uri => imageQuadrants(page, uri), handoff.transcodedImageType)
    return
  }
  const content = nativeUserStrings(request.body)
  if (content.length === 0)
    throw new Error('the native request has no user content for the attachment')
  const joined = content.join('\n')
  if (kind === 'text') {
    expect(joined, 'the native user content must include the full text file').toContain(source.toString('utf8'))
    return
  }
  if (!joined.includes(source.toString('base64')))
    throw new Error('the native user content omits the complete binary file')
}

/**
 * Verify that one attachment reaches the provider's model request and the chat.
 *
 * `protocol` states the model API that the provider uses. When it is set, a
 * request on another API fails the proof. `transcodedImageType` states the
 * image format that a transcoding provider writes (see `ImageHandoff`).
 */
export async function exerciseAttachmentDelivery(
  page: Page,
  modelScript: ModelScript,
  kind: AttachmentKind,
  fileName: string,
  options: { fixturePath?: string, protocol?: MockModelProtocol } & ImageHandoff = {},
): Promise<void> {
  const { protocol, transcodedImageType, ...outcome } = options
  // The step that this helper queues holds the attachment turn, also when the
  // test queued other steps first.
  const stepIndex = await modelScript.queue({ text: 'Attachment received.' })
  const sourcePath = await expectAttachmentOutcome(page, kind, { supported: true, fileName, ...outcome })
  await sendWithAttachment(page, modelScript.prompt('Inspect the attached file.'))
  const status = await modelScript.waitForSteps()
  await waitForAgentIdle(page)

  const handoff: ImageHandoff = transcodedImageType === undefined ? {} : { transcodedImageType }
  await expectAttachmentInRequest(page, scriptedRequest(status, protocol, stepIndex), kind, sourcePath, handoff)
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
  const cleanStepIndex = await modelScript.queue(response)
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
