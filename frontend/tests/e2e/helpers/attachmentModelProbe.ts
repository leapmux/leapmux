import type { Page } from '@playwright/test'
import type { AttachmentKind } from './attachments'
import type { MockModelProtocol, MockModelRequestRecord, MockModelScenarioStatus } from './mockModelScript'
import type { NativeScenarioContext } from './nativeScenario'
import { Buffer } from 'node:buffer'
import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { basename } from 'node:path'
import { expect } from '@playwright/test'
import { isObject, pickObject } from '../../../src/lib/jsonPick'
import { AMP_ACTOR_PATH_PREFIX } from './ampSurface'
import { expectAttachmentOutcome, PDF_PAGE_MARKER, sendWithAttachment } from './attachments'
import { CURSOR_RUN_PATH } from './cursorSurface'
import { jsonStringValues } from './jsonStringValues'
import { kiroCurrentUserInput } from './kiroSurface'
import { stepRequest } from './mockModelScript'
import { nativeTextStep } from './nativeScenario'
import { assistantBubbles, sendMessage, userBubbles, waitForAgentIdle } from './ui'

const CHANNEL_TOLERANCE = 48
const EXPECTED_QUADRANTS = [
  [255, 0, 0],
  [0, 255, 0],
  [0, 0, 255],
  [255, 255, 0],
]

/** Read the user content of a scripted native request. */
export function nativeUserStrings(body: unknown): string[] {
  if (!isObject(body))
    return []
  const parts: unknown[] = []
  for (const field of ['messages', 'input']) {
    const rows = body[field]
    if (!Array.isArray(rows))
      continue
    for (const row of rows) {
      if (isObject(row) && row.role === 'user')
        parts.push(row.content ?? row)
    }
  }
  if (Array.isArray(body.contents)) {
    for (const row of body.contents) {
      if (!isObject(row) || row.role !== 'user' || !Array.isArray(row.parts))
        continue
      for (const part of row.parts) {
        if (isObject(part) && (typeof part.text === 'string' || isObject(part.inlineData)))
          parts.push(part)
      }
    }
  }
  if (typeof body.input === 'string')
    parts.push(body.input)
  const kiroUser = kiroCurrentUserInput(body)
  if (kiroUser) {
    for (const field of ['content', 'images', 'attachments', 'documents', 'documentAttachments']) {
      if (kiroUser[field] !== undefined)
        parts.push(kiroUser[field])
    }
  }
  if (body.prompt !== undefined)
    parts.push(body.prompt)
  if (body.attachments !== undefined)
    parts.push(body.attachments)
  return parts.flatMap(jsonStringValues)
}

/** Select the native model request that consumed one ordered step of the script. */
export function scriptedRequest(status: MockModelScenarioStatus, protocol?: MockModelProtocol, stepIndex = 0): MockModelRequestRecord {
  const request = stepRequest(status, stepIndex)
  if (protocol)
    expect(request.protocol, 'the scripted request uses the model API of the provider').toBe(protocol)
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
    const row: unknown = rows[index]
    const role = isObject(row) ? row.role : undefined
    if (INSTRUCTION_ROLES.has(role))
      continue
    if (!isObject(row) || role !== 'user') {
      const firstInTurn = turn[0]
      if (role === 'tool' && firstInTurn && firstInTurn[0] === index + 1)
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
 * Chat Completions carries a file as `file.file_data` and an image as
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

/**
 * Read the source of an Anthropic-shaped content block. Only a base64 source carries the bytes.
 * Anthropic spells the media type field `media_type`. Amp's own block spells it `mediaType`.
 */
function anthropicSourcePart(location: string, value: unknown, mediaTypeField: 'media_type' | 'mediaType'): TypedFilePart {
  if (!isObject(value))
    return { location, defect: 'has no source' }
  if (value.type !== 'base64')
    return { location, defect: `has a source of type ${String(value.type)}, not base64 bytes` }
  const mediaType = value[mediaTypeField]
  return { location, declaredType: `${mediaTypeField}=${String(mediaType)}`, mediaType: mediaTypeOf(mediaType), data: value.data }
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
  for (const [index, row] of currentUserTurn(isObject(body) ? body[rowsField] : undefined)) {
    const content = row[contentField]
    if (!Array.isArray(content) || answersToolCall(content))
      continue
    content.forEach((value: unknown, position) => {
      const part = isObject(value) ? read(value, `${rowsField}[${index}].${contentField}[${position}]`) : undefined
      if (part)
        parts.push(part)
    })
  }
  return parts
}

/** Anthropic Messages: a `document` block with a base64 source in a user message that answers no tool call. */
function anthropicFileParts(body: unknown): TypedFilePart[] {
  return currentTurnContentParts(body, 'messages', 'content', (block, location) =>
    block.type === 'document' ? anthropicSourcePart(location, block.source, 'media_type') : undefined, holdsAnthropicToolResult)
}

/** Chat Completions: a `file` part with `file.file_data` in a user message. */
function chatCompletionFileParts(body: unknown): TypedFilePart[] {
  return currentTurnContentParts(body, 'messages', 'content', (part, location) =>
    part.type === 'file' ? dataURIPart(location, 'file.file_data', pickObject(part, 'file')?.file_data) : undefined)
}

/** Read the entries of one file list of Kiro's current user input message. */
function kiroFileParts(user: Record<string, unknown> | undefined, field: 'documents' | 'images'): TypedFilePart[] {
  const entries = user?.[field]
  if (!Array.isArray(entries))
    return []
  return entries.map((entry: unknown, position) => {
    const fields: Record<string, unknown> = isObject(entry) ? entry : {}
    return {
      location: `conversationState.currentMessage.userInputMessage.${field}[${position}]`,
      declaredType: `format=${String(fields.format)}`,
      mediaType: KIRO_FORMAT_MEDIA_TYPES.get(fields.format),
      data: pickObject(fields, 'source')?.bytes,
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
  const inline = pickObject(part, 'inlineData')
  if (!inline)
    return undefined
  return { location, declaredType: `mimeType=${String(inline.mimeType)}`, mediaType: mediaTypeOf(inline.mimeType), data: inline.data }
}

/**
 * Google Generative Language: an `inlineData` part in a user content that answers no function call.
 * Google carries a file and an image in the same part shape, so both proofs read these parts.
 */
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
      throw new Error('no provider that sends a typed file part uses the openai-responses protocol, so the file proof has no reader for it')
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

/** The media type that a typed file part must declare, and the name that a failure gives it. */
interface FileTypeExpectation {
  label: string
  accepts: (mediaType: string | undefined) => boolean
}

/** Return why a typed part does not carry the exact source file with an accepted type, or undefined when it does. */
function filePartDefect(part: TypedFilePart, source: Buffer, expected: FileTypeExpectation): string | undefined {
  if ('defect' in part)
    return part.defect
  if (!expected.accepts(part.mediaType))
    return `declares ${part.declaredType}, not ${expected.label}`
  const decoded = decodeCanonicalBase64(part.data)
  if ('defect' in decoded)
    return decoded.defect
  return sourceBytesDefect(decoded.bytes, source)
}

/** Require the exact source file in a typed file part of the current user turn, and state each part that came close. */
function expectTypedFilePart(request: MockModelRequestRecord, source: Buffer, expected: FileTypeExpectation): void {
  const defects: string[] = []
  for (const part of typedFileParts(request.protocol, request.body)) {
    const defect = filePartDefect(part, source, expected)
    if (defect === undefined)
      return
    defects.push(`${part.location} ${defect}`)
  }
  const detail = defects.length > 0 ? `: ${defects.join('; ')}` : ''
  throw new Error(`the current user turn of the scripted ${request.protocol} request carries no typed ${expected.label} part with the exact source bytes${detail}`)
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
  expectTypedFilePart(request, source, { label: 'PDF', accepts: mediaType => mediaType === PDF_MEDIA_TYPE })
}

/**
 * The media type that the typed part of a file must declare: the exact type, or an anchored pattern.
 *
 * Use a pattern only for a type that the test cannot state. The browser takes the declared type of a file with no
 * known extension from the platform: on macOS a `.bin` file declares `application/macbinary`.
 */
export type DeclaredMediaType = string | RegExp

/**
 * Require the exact source file in a typed file part of the request's current user turn.
 *
 * The part must declare a media type that `mediaType` accepts and carry the exact
 * source bytes in canonical base64. As with a PDF (see `expectNativePdfPart`), the
 * part proves that the agent received the original bytes and gave the model a file.
 * The complete base64 inside text or a data URI inside text does not count.
 */
export function expectNativeFilePart(request: MockModelRequestRecord, source: Buffer, mediaType: DeclaredMediaType): void {
  if (typeof mediaType === 'string') {
    if (mediaType === '')
      throw new Error('a file part proof needs the media type that the part declares')
    expectTypedFilePart(request, source, { label: mediaType, accepts: declared => declared === mediaType })
    return
  }
  // A global or sticky pattern keeps its last match position between calls, so one part could change the result
  // for the next part.
  if (mediaType.global || mediaType.sticky)
    throw new Error('a file part proof needs a media type pattern without the g or y flag')
  expectTypedFilePart(request, source, { label: String(mediaType), accepts: declared => declared !== undefined && mediaType.test(declared) })
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
  return content.some(block => isObject(block) && block.type === 'tool_result')
}

/** A Google user content that holds a `functionResponse` part answers a function call. */
function holdsGoogleFunctionResponse(content: unknown[]): boolean {
  return content.some(part => isObject(part) && isObject(part.functionResponse))
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
    block.type === 'image' ? anthropicSourcePart(location, block.source, 'media_type') : undefined, holdsAnthropicToolResult)
}

/**
 * Amp's thread message: an `image` block in Amp's own shape in a user message.
 *
 * The mock records the content that Amp appends to its thread, unchanged. Amp's block
 * spells the media type `mediaType` and adds `sourcePath`, unlike an Anthropic block
 * (see `providers/amp/attachments.go` in the backend). A message that holds a tool result stays out.
 */
function ampImageParts(body: unknown): TypedFilePart[] {
  return currentTurnContentParts(body, 'messages', 'content', (block, location) =>
    block.type === 'image' ? anthropicSourcePart(location, block.source, 'mediaType') : undefined, holdsAnthropicToolResult)
}

/** Chat Completions: an `image_url` part with a base64 data URI in a user message. A tool result is a `tool` row, which ends the turn. */
function chatCompletionImageParts(body: unknown): TypedFilePart[] {
  return currentTurnContentParts(body, 'messages', 'content', (part, location) =>
    part.type === 'image_url' ? dataURIPart(location, 'image_url.url', pickObject(part, 'image_url')?.url) : undefined)
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
  const attachments = isObject(body) ? body.attachments : undefined
  if (!Array.isArray(attachments))
    return []
  const parts: TypedFilePart[] = []
  attachments.forEach((attachment: unknown, position) => {
    if (isObject(attachment) && attachment.kind === 'image')
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
  const toolResults = pickObject(user, 'userInputMessageContext')?.toolResults
  if (Array.isArray(toolResults) && toolResults.length > 0)
    return []
  return kiroFileParts(user, 'images')
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
      return googleInlineParts(request.body)
  }
}

/** Decode an image data URI and return the RGBA pixel at the center of each quadrant, in reading order. */
export type QuadrantDecoder = (dataURI: string) => Promise<number[][]>

/** Return why a typed part does not carry the exact source image, or undefined when it does. */
function exactImagePartDefect(part: TypedFilePart, source: Buffer, sourceType: ImageMediaType): string | undefined {
  return filePartDefect(part, source, { label: sourceType, accepts: mediaType => mediaType === sourceType })
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

/** How a provider hands an attachment to its model. Each field applies to one attachment kind. */
export interface AttachmentHandoff {
  /**
   * For an image: the media type that the provider re-encodes the image into
   * before its model request. Leave it absent for a provider that sends the
   * source bytes unchanged. Set it only from evidence that the provider
   * transcodes, because it replaces the exact-byte proof with a decoded-color
   * proof.
   */
  transcodedImageType?: ImageMediaType
  /**
   * For a binary file: the media type that its typed file part declares (see
   * `expectNativeFilePart`). A binary proof needs it, because only a typed part
   * gives the model a file.
   */
  binaryMediaType?: DeclaredMediaType
}

/** Return the media type of a binary proof, or refuse a binary proof without one. */
function binaryMediaType(handoff: AttachmentHandoff): DeclaredMediaType {
  if (handoff.binaryMediaType === undefined)
    throw new Error('a binary attachment proof needs the media type that its typed file part declares')
  return handoff.binaryMediaType
}

/** Refuse a handoff field that does not apply to the attachment kind, and a binary proof without its media type. */
function validateHandoff(kind: AttachmentKind, handoff: AttachmentHandoff): void {
  if (handoff.transcodedImageType !== undefined && kind !== 'image')
    throw new Error(`a transcoded image type applies to an image attachment, not to a ${kind} attachment`)
  if (handoff.binaryMediaType !== undefined && kind !== 'binary')
    throw new Error(`a binary media type applies to a binary attachment, not to a ${kind} attachment`)
  if (kind === 'binary')
    binaryMediaType(handoff)
}

/** How `expectNativeAttachmentProof` selects the request and reads the handoff. */
export interface AttachmentProofOptions extends AttachmentHandoff {
  /** The model API of the provider. A request on another API fails the proof. */
  protocol?: MockModelProtocol
  /**
   * The ordered step whose request carries the attachment: the first step by
   * default. A test that runs a turn before the attachment turn measures this
   * index from the index that `ModelScript.queue` returns.
   */
  stepIndex?: number
}

/**
 * Prove the source file's content in the scripted native user request.
 *
 * A PDF needs a typed PDF part with the exact source bytes (see
 * `expectNativePdfPart`). An image needs a typed image part of the current user
 * turn (see `expectNativeImagePart`). A binary file needs a typed file part (see
 * `expectNativeFilePart`). A text file must reach the user content whole.
 */
export async function expectNativeAttachmentProof(
  page: Page,
  status: MockModelScenarioStatus,
  kind: AttachmentKind,
  sourcePath: string,
  options: AttachmentProofOptions = {},
): Promise<void> {
  const { protocol, stepIndex = 0, ...handoff } = options
  await expectAttachmentInRequest(page, scriptedRequest(status, protocol, stepIndex), kind, sourcePath, handoff)
}

async function expectAttachmentInRequest(page: Page, request: MockModelRequestRecord, kind: AttachmentKind, sourcePath: string, handoff: AttachmentHandoff): Promise<void> {
  validateHandoff(kind, handoff)
  const source = readFileSync(sourcePath)
  switch (kind) {
    case 'pdf':
      expectNativePdfPart(request, source)
      return
    case 'image':
      await expectNativeImagePart(request, source, uri => imageQuadrants(page, uri), handoff.transcodedImageType)
      return
    case 'binary':
      expectNativeFilePart(request, source, binaryMediaType(handoff))
      return
    case 'text': {
      const content = nativeUserStrings(request.body)
      if (content.length === 0)
        throw new Error('the native request has no user content for the attachment')
      expect(content.join('\n'), 'the native user content must include the full text file').toContain(source.toString('utf8'))
    }
  }
}

/** How `exerciseAttachmentDelivery` attaches the file and proves its handoff. */
export interface AttachmentDeliveryOptions extends AttachmentHandoff {
  /** A caller's fixture file, for a provider that needs another valid file. Its base name must be the file name. */
  fixturePath?: string
  /** The model API of the provider. A request on another API fails the proof. */
  protocol?: MockModelProtocol
  /**
   * A proof that replaces the typed-part proof, for a provider that hands the
   * file over in its own shape (Grok copies the file and states its path). It
   * excludes the handoff fields, which only the typed-part proof reads.
   */
  proof?: (request: MockModelRequestRecord, sourcePath: string) => void | Promise<void>
}

/** The answer of the attachment turn. */
const DELIVERY_ANSWER = 'Attachment received.'

/** The prompt of the attachment turn. */
const DELIVERY_PROMPT = 'Inspect the attached file.'

/**
 * Verify that one attachment reaches the provider's model request and the chat.
 *
 * The turn queues its answer through the context's text step, attaches the file,
 * sends it with the prompt, and waits for the agent. It then proves the handoff
 * in the request of its own step, and requires one user row with the file name
 * and the prompt, and the answer. It returns that request.
 */
export async function exerciseAttachmentDelivery(
  context: NativeScenarioContext,
  kind: AttachmentKind,
  fileName: string,
  options: AttachmentDeliveryOptions = {},
): Promise<MockModelRequestRecord> {
  const { protocol, proof, fixturePath, ...handoff } = options
  if (proof !== undefined && (handoff.transcodedImageType !== undefined || handoff.binaryMediaType !== undefined))
    throw new Error('a custom attachment proof replaces the typed-part proof, so it takes no transcoded image type and no binary media type')
  if (proof === undefined)
    validateHandoff(kind, handoff)
  const { page, modelScript } = context
  // The step that this helper queues holds the attachment turn, also when the
  // test queued other steps first.
  const stepIndex = await modelScript.queue(nativeTextStep(context, DELIVERY_ANSWER))
  const sourcePath = await expectAttachmentOutcome(page, kind, { supported: true, fileName, ...(fixturePath === undefined ? {} : { fixturePath }) })
  await sendWithAttachment(page, modelScript.prompt(DELIVERY_PROMPT))
  await modelScript.waitForSteps(stepIndex + 1)
  await waitForAgentIdle(page)

  // Read the record after the turn ends. A native client can add to its request after the mock counts the step.
  const request = await modelScript.requestAt(stepIndex)
  if (protocol !== undefined)
    expect(request.protocol, 'the attachment request uses the model API of the provider').toBe(protocol)
  if (proof === undefined)
    await expectAttachmentInRequest(page, request, kind, sourcePath, handoff)
  else
    await proof(request, sourcePath)
  await expect(userBubbles(page).filter({ hasText: fileName }).filter({ hasText: DELIVERY_PROMPT }).first()).toBeVisible()
  await expect(assistantBubbles(page).filter({ hasText: DELIVERY_ANSWER }).first()).toBeVisible()
  return request
}

/** A refused attachment: its kind, or its kind and the name of its fixture file. */
export type RefusedAttachment = AttachmentKind | { kind: AttachmentKind, fileName: string }

/**
 * Attach each refused file and require that the composer refuses it, then send
 * a clean turn and require that none of the refused content reaches the model
 * (see `expectRefusedAttachmentsAbsent`). Return the request of the clean turn.
 */
export async function exerciseAttachmentRefusal(
  context: NativeScenarioContext,
  ...refused: [RefusedAttachment, ...RefusedAttachment[]]
): Promise<MockModelRequestRecord> {
  const rejectedPaths: string[] = []
  for (const attachment of refused) {
    const { kind, fileName } = typeof attachment === 'string' ? { kind: attachment, fileName: undefined } : attachment
    rejectedPaths.push(await expectAttachmentOutcome(context.page, kind, { supported: false, ...(fileName === undefined ? {} : { fileName }) }))
  }
  return expectRefusedAttachmentsAbsent(context, rejectedPaths)
}

/** The prompt of the clean turn after a refusal. */
const CLEAN_PROMPT = 'Reply once without attachments.'

/** The answer of the clean turn after a refusal. */
const CLEAN_ANSWER = 'The clean prompt answered.'

/**
 * Send a clean turn after a refusal and require the rejected content to stay out.
 * The answer goes through the context's text step. Return the request of the clean turn.
 */
export async function expectRefusedAttachmentsAbsent(
  context: NativeScenarioContext,
  rejectedPaths: readonly string[],
): Promise<MockModelRequestRecord> {
  if (rejectedPaths.length === 0)
    throw new Error('a refusal proof needs at least one refused file')
  const { page, modelScript } = context
  const stepIndex = await modelScript.queue(nativeTextStep(context, CLEAN_ANSWER))
  await sendMessage(page, modelScript.prompt(CLEAN_PROMPT))
  await modelScript.waitForSteps(stepIndex + 1)
  await waitForAgentIdle(page)
  const request = await modelScript.requestAt(stepIndex)
  await expectNoRejectedContentIn(page, request, rejectedPaths)
  await expect(assistantBubbles(page).filter({ hasText: CLEAN_ANSWER }).first()).toBeVisible()
  return request
}

/** Check the clean request of the step at `stepIndex` for rejected content after a refusal. The last step by default. */
export async function expectNoRejectedContent(page: Page, status: MockModelScenarioStatus, rejectedPaths: readonly string[], stepIndex = status.stepCount - 1): Promise<void> {
  await expectNoRejectedContentIn(page, scriptedRequest(status, undefined, stepIndex), rejectedPaths)
}

/**
 * Require the clean prompt in the user content of `request`, and require that no rejected file reaches the request:
 * not its name in the user content, not its bytes in base64 anywhere, not the page text of a PDF, not the text of a
 * text file, and not an image that decodes to the colors of a rejected image or PDF.
 */
async function expectNoRejectedContentIn(page: Page, request: MockModelRequestRecord, rejectedPaths: readonly string[]): Promise<void> {
  const userContent = nativeUserStrings(request.body).join('\n')
  const requestStrings = jsonStringValues(request.body)
  const requestContent = requestStrings.join('\n')
  expect(userContent).toContain(CLEAN_PROMPT)
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
