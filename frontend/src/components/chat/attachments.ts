/** Attachment types, cache, and helpers for the agent editor panel. */

import type { AttachmentCapabilities } from './providers/registry'
import type { AttachmentKind } from '~/generated/contracts/attachment-media'
import { MAX_AGENT_INPUT_ITEM_BYTES } from '~/generated/contracts/agent-input'
import { ATTACHMENT_MEDIA_AUDIO_MIME, ATTACHMENT_MEDIA_DOCUMENT_MIME, ATTACHMENT_MEDIA_IMAGE_MIME, ATTACHMENT_MEDIA_KIND } from '~/generated/contracts/attachment-media'
import { randomUUID } from '~/lib/idGenerator'
import { extname } from '~/lib/paths'
import { queueEditDraftKeyPrefix } from './composer/draftIdentity'

const LEADING_SLASHES = /^\/+/

export interface FileAttachment {
  id: string
  file: File
  filename: string
  mimeType: string
  data: Uint8Array
  size: number
}

export interface PendingAttachmentFile {
  file: File
  filename?: string
}

export interface AttachmentDetails {
  kind: AttachmentKind
  mimeType: string
}

/**
 * Maximum total size of every attachment on one composer draft.
 *
 * The number comes from contracts/agent-input.json, because the Worker
 * enforces the same cap in Store.Enqueue. Before the contract the two sides
 * held their own copies and could drift apart silently.
 *
 * This check stays advisory, and deliberately so. The Worker measures the
 * whole item -- the text bytes PLUS the attachment bytes -- and the user can
 * type more text after the last attachment lands, so no client check can be
 * the authority. It exists to refuse the obvious case early, with a message
 * that names the same limit the Worker states.
 */
export const MAX_TOTAL_ATTACHMENT_SIZE = MAX_AGENT_INPUT_ITEM_BYTES

type WebkitDataTransferItem = DataTransferItem & {
  webkitGetAsEntry?: () => FileSystemEntry | null
}

interface FileSystemEntry {
  readonly isFile: boolean
  readonly isDirectory: boolean
  readonly name: string
  readonly fullPath: string
}

interface FileSystemFileEntry extends FileSystemEntry {
  file: (successCallback: (file: File) => void, errorCallback?: (err: DOMException) => void) => void
}

interface FileSystemDirectoryEntry extends FileSystemEntry {
  createReader: () => FileSystemDirectoryReader
}

interface FileSystemDirectoryReader {
  readEntries: (
    successCallback: (entries: FileSystemEntry[]) => void,
    errorCallback?: (err: DOMException) => void,
  ) => void
}

const SUPPORTED_IMAGE_MIME_TYPES = new Set<string>(Object.values(ATTACHMENT_MEDIA_IMAGE_MIME))

const GENERIC_BINARY_MIME_TYPES = new Set([
  '',
  'application/octet-stream',
])

const TEXT_MIME_BY_EXTENSION = new Map<string, string>([
  ['txt', 'text/plain'],
  ['md', 'text/markdown'],
  ['markdown', 'text/markdown'],
  ['csv', 'text/csv'],
  ['tsv', 'text/tab-separated-values'],
  ['css', 'text/css'],
  ['html', 'text/html'],
  ['htm', 'text/html'],
  ['xml', 'application/xml'],
  ['json', 'application/json'],
  ['jsonc', 'application/json'],
  ['yaml', 'application/yaml'],
  ['yml', 'application/yaml'],
  ['toml', 'application/toml'],
  ['ini', 'text/plain'],
  ['cfg', 'text/plain'],
  ['conf', 'text/plain'],
  ['env', 'text/plain'],
  ['log', 'text/plain'],
  ['sh', 'text/x-shellscript'],
  ['bash', 'text/x-shellscript'],
  ['zsh', 'text/x-shellscript'],
  ['fish', 'text/x-shellscript'],
  ['js', 'text/javascript'],
  ['mjs', 'text/javascript'],
  ['cjs', 'text/javascript'],
  ['ts', 'text/typescript'],
  ['tsx', 'text/typescript'],
  ['jsx', 'text/javascript'],
  ['py', 'text/x-python'],
  ['rb', 'text/plain'],
  ['go', 'text/plain'],
  ['rs', 'text/plain'],
  ['java', 'text/plain'],
  ['kt', 'text/plain'],
  ['swift', 'text/plain'],
  ['c', 'text/plain'],
  ['cc', 'text/plain'],
  ['cpp', 'text/plain'],
  ['cxx', 'text/plain'],
  ['h', 'text/plain'],
  ['hh', 'text/plain'],
  ['hpp', 'text/plain'],
  ['sql', 'text/plain'],
  ['graphql', 'application/graphql'],
  ['gql', 'application/graphql'],
  ['dockerfile', 'text/plain'],
  ['gitignore', 'text/plain'],
  ['editorconfig', 'text/plain'],
  ['svg', 'image/svg+xml'],
])

const BINARY_MIME_BY_EXTENSION = new Map<string, string>([
  ['png', ATTACHMENT_MEDIA_IMAGE_MIME.Png],
  ['jpg', ATTACHMENT_MEDIA_IMAGE_MIME.Jpeg],
  ['jpeg', ATTACHMENT_MEDIA_IMAGE_MIME.Jpeg],
  ['gif', ATTACHMENT_MEDIA_IMAGE_MIME.Gif],
  ['webp', ATTACHMENT_MEDIA_IMAGE_MIME.Webp],
  ['pdf', ATTACHMENT_MEDIA_DOCUMENT_MIME.Pdf],
  ['doc', ATTACHMENT_MEDIA_DOCUMENT_MIME.Doc],
  ['docx', ATTACHMENT_MEDIA_DOCUMENT_MIME.Docx],
  ['xls', ATTACHMENT_MEDIA_DOCUMENT_MIME.Xls],
  ['xlsx', ATTACHMENT_MEDIA_DOCUMENT_MIME.Xlsx],
  ['wav', ATTACHMENT_MEDIA_AUDIO_MIME.Wav],
  ['mp3', ATTACHMENT_MEDIA_AUDIO_MIME.Mpeg],
  ['m4a', ATTACHMENT_MEDIA_AUDIO_MIME.Mp4],
  ['webm', ATTACHMENT_MEDIA_AUDIO_MIME.Webm],
  ['ogg', ATTACHMENT_MEDIA_AUDIO_MIME.Ogg],
  ['flac', ATTACHMENT_MEDIA_AUDIO_MIME.Flac],
  ['aac', ATTACHMENT_MEDIA_AUDIO_MIME.Aac],
])

const TEXT_FILE_EXTENSIONS = Array.from(TEXT_MIME_BY_EXTENSION.keys(), ext => `.${ext}`)

const TEXTUAL_MIME_PREFIXES = ['text/']
const TEXTUAL_MIME_EXACT = new Set([
  'application/json',
  'application/xml',
  'application/yaml',
  'application/toml',
  'application/graphql',
  'image/svg+xml',
])

/** File input accept attribute for provider-specific supported types. */
export function buildAcceptAttribute(capabilities: AttachmentCapabilities | undefined): string | undefined {
  if (!capabilities)
    return undefined
  if (capabilities.binary === true)
    return undefined

  const accepted = new Set<string>()
  if (capabilities.text) {
    for (const ext of TEXT_FILE_EXTENSIONS)
      accepted.add(ext)
  }
  if (capabilities.image) {
    for (const mime of SUPPORTED_IMAGE_MIME_TYPES)
      accepted.add(mime)
  }
  if (capabilities.pdf)
    accepted.add(ATTACHMENT_MEDIA_DOCUMENT_MIME.Pdf)
  if (capabilities.audio === true) {
    accepted.add('audio/*')
  }
  else if (typeof capabilities.audio !== 'boolean') {
    for (const mime of capabilities.audio)
      accepted.add(mime)
  }
  if (typeof capabilities.binary !== 'boolean') {
    for (const mime of capabilities.binary)
      accepted.add(mime)
  }

  return accepted.size > 0 ? [...accepted].join(',') : undefined
}

function normalizeMimeType(mime: string): string {
  return mime.trim().toLowerCase()
}

function inferredMimeTypeFromFilename(filename: string): string {
  const lower = filename.trim().toLowerCase()
  if (lower === 'dockerfile')
    return 'text/plain'
  if (lower === '.gitignore' || lower === '.editorconfig' || lower === '.env')
    return 'text/plain'
  return TEXT_MIME_BY_EXTENSION.get(extname(filename)) ?? BINARY_MIME_BY_EXTENSION.get(extname(filename)) ?? ''
}

function isTextualMimeType(mime: string): boolean {
  if (TEXTUAL_MIME_EXACT.has(mime))
    return true
  if (mime.endsWith('+json') || mime.endsWith('+xml'))
    return true
  return TEXTUAL_MIME_PREFIXES.some(prefix => mime.startsWith(prefix))
}

function isUtf8Text(data: Uint8Array): boolean {
  try {
    new TextDecoder('utf-8', { fatal: true }).decode(data)
    return true
  }
  catch {
    return false
  }
}

export function inferAttachmentDetails(filename: string, mimeType: string, data: Uint8Array): AttachmentDetails {
  const normalizedMime = normalizeMimeType(mimeType)
  const inferredMime = inferredMimeTypeFromFilename(filename)
  let utf8: boolean | undefined
  const isTextData = () => utf8 ??= isUtf8Text(data)
  const effectiveMime = !GENERIC_BINARY_MIME_TYPES.has(normalizedMime)
    ? normalizedMime
    : (inferredMime || (isTextData() ? 'text/plain' : 'application/octet-stream'))

  if (SUPPORTED_IMAGE_MIME_TYPES.has(effectiveMime))
    return { kind: ATTACHMENT_MEDIA_KIND.Image, mimeType: effectiveMime }

  if (effectiveMime === ATTACHMENT_MEDIA_DOCUMENT_MIME.Pdf)
    return { kind: ATTACHMENT_MEDIA_KIND.Pdf, mimeType: effectiveMime }

  if (effectiveMime.startsWith('audio/'))
    return { kind: ATTACHMENT_MEDIA_KIND.Audio, mimeType: effectiveMime }

  if (isTextualMimeType(effectiveMime) && isTextData())
    return { kind: ATTACHMENT_MEDIA_KIND.Text, mimeType: effectiveMime }

  if (GENERIC_BINARY_MIME_TYPES.has(normalizedMime) && (!inferredMime || isTextualMimeType(inferredMime)) && isTextData())
    return { kind: ATTACHMENT_MEDIA_KIND.Text, mimeType: inferredMime || 'text/plain' }

  return { kind: ATTACHMENT_MEDIA_KIND.Binary, mimeType: effectiveMime || 'application/octet-stream' }
}

function acceptsMediaType(policy: boolean | readonly string[], mimeType: string | undefined): boolean {
  return typeof policy === 'boolean' ? policy : policy.includes(normalizeMimeType(mimeType ?? ''))
}

export function isAttachmentSupported(kind: AttachmentKind, capabilities: AttachmentCapabilities | undefined, mimeType?: string): boolean {
  if (!capabilities)
    return kind !== ATTACHMENT_MEDIA_KIND.Audio
  switch (kind) {
    case ATTACHMENT_MEDIA_KIND.Text: return capabilities.text
    case ATTACHMENT_MEDIA_KIND.Image: return capabilities.image
    case ATTACHMENT_MEDIA_KIND.Pdf: return capabilities.pdf
    case ATTACHMENT_MEDIA_KIND.Audio: return acceptsMediaType(capabilities.audio, mimeType)
    case ATTACHMENT_MEDIA_KIND.Binary: return acceptsMediaType(capabilities.binary, mimeType)
  }
}

export function describeUnsupportedAttachment(kind: AttachmentKind, providerLabel: string, capabilities?: AttachmentCapabilities): string {
  switch (kind) {
    case ATTACHMENT_MEDIA_KIND.Pdf:
      return `${providerLabel} does not support PDF attachments`
    case ATTACHMENT_MEDIA_KIND.Audio:
      return capabilities && typeof capabilities.audio !== 'boolean'
        ? `${providerLabel} does not support this audio file type`
        : `${providerLabel} does not support audio attachments`
    case ATTACHMENT_MEDIA_KIND.Binary:
      return capabilities && typeof capabilities.binary !== 'boolean'
        ? `${providerLabel} does not support this binary file type`
        : `${providerLabel} does not support binary attachments`
    case ATTACHMENT_MEDIA_KIND.Image:
      return `${providerLabel} does not support image attachments`
    case ATTACHMENT_MEDIA_KIND.Text:
      return `${providerLabel} does not support text attachments`
  }
}

export function isImageMimeType(mime: string): boolean {
  return SUPPORTED_IMAGE_MIME_TYPES.has(normalizeMimeType(mime))
}

export function totalAttachmentSize(attachments: FileAttachment[]): number {
  return attachments.reduce((sum, a) => sum + a.size, 0)
}

async function fileFromEntry(entry: FileSystemFileEntry): Promise<File> {
  return new Promise((resolve, reject) => {
    entry.file(resolve, reject)
  })
}

async function readAllDirectoryEntries(entry: FileSystemDirectoryEntry): Promise<{ entries: FileSystemEntry[], failed: boolean }> {
  const reader = entry.createReader()
  const entries: FileSystemEntry[] = []
  while (true) {
    let batch: FileSystemEntry[]
    try {
      batch = await new Promise<FileSystemEntry[]>((resolve, reject) => {
        reader.readEntries(resolve, reject)
      })
    }
    catch {
      return { entries, failed: true }
    }
    if (batch.length === 0)
      return { entries, failed: false }
    entries.push(...batch)
  }
}

function sortedEntries(entries: FileSystemEntry[]): FileSystemEntry[] {
  return entries.toSorted((a, b) => a.name.localeCompare(b.name))
}

interface AttachmentReadFailure {
  path?: string
}

interface DroppedFileCollection {
  files: PendingAttachmentFile[]
  size: number
  failures: AttachmentReadFailure[]
}

async function collectFilesFromEntry(entry: FileSystemEntry, collection: DroppedFileCollection): Promise<boolean> {
  const path = entry.fullPath.replace(LEADING_SLASHES, '') || entry.name
  try {
    if (entry.isFile) {
      const file = await fileFromEntry(entry as FileSystemFileEntry)
      if (collection.size + file.size > MAX_TOTAL_ATTACHMENT_SIZE)
        return true
      collection.size += file.size
      collection.files.push({ file, filename: path })
      return false
    }
    if (!entry.isDirectory)
      return false

    const { entries, failed } = await readAllDirectoryEntries(entry as FileSystemDirectoryEntry)
    if (failed)
      collection.failures.push({ path })
    for (const childEntry of sortedEntries(entries)) {
      if (await collectFilesFromEntry(childEntry, collection))
        return true
    }
  }
  catch {
    collection.failures.push({ path })
  }
  return false
}

/**
 * Collect dropped files and retain readable entries when another entry fails.
 * Traversal stops when the next file exceeds the global attachment size limit.
 */
export async function collectDroppedAttachmentFiles(
  dataTransfer: DataTransfer,
  currentSize = 0,
): Promise<{ files: PendingAttachmentFile[], sizeLimitHit: boolean, failures: AttachmentReadFailure[] }> {
  const collection: DroppedFileCollection = { files: [], size: currentSize, failures: [] }
  const items = [...(dataTransfer.items ?? [])] as WebkitDataTransferItem[]
  const entries: FileSystemEntry[] = []
  for (const item of items) {
    try {
      const entry = item.webkitGetAsEntry?.()
      if (entry)
        entries.push(entry)
    }
    catch {
      collection.failures.push({})
    }
  }
  let sizeLimitHit = false
  if (entries.length > 0 || collection.failures.length > 0) {
    for (const entry of sortedEntries(entries)) {
      if (await collectFilesFromEntry(entry, collection)) {
        sizeLimitHit = true
        break
      }
    }
  }
  else {
    for (const file of [...dataTransfer.files]) {
      if (collection.size + file.size > MAX_TOTAL_ATTACHMENT_SIZE) {
        sizeLimitHit = true
        break
      }
      collection.size += file.size
      collection.files.push({ file })
    }
  }
  return { files: collection.files, sizeLimitHit, failures: collection.failures }
}

// ---------------------------------------------------------------------------
// Per-agent attachment cache (in-memory, keyed by agentId)
// ---------------------------------------------------------------------------

const attachmentCache = new Map<string, FileAttachment[]>()
const pastedImageCounters = new Map<string, number>()

export function getAttachments(agentId: string): FileAttachment[] {
  return attachmentCache.get(agentId) ?? []
}

export function setAttachments(agentId: string, attachments: FileAttachment[]): void {
  attachmentCache.set(agentId, attachments)
}

export function clearAttachments(agentId: string): void {
  attachmentCache.delete(agentId)
  pastedImageCounters.delete(agentId)
}

export function forgetAgentAttachments(agentId: string): void {
  const queuePrefix = queueEditDraftKeyPrefix(agentId)
  for (const key of attachmentCache.keys()) {
    if (key === agentId || key.startsWith(queuePrefix))
      attachmentCache.delete(key)
  }
  for (const key of pastedImageCounters.keys()) {
    if (key === agentId || key.startsWith(queuePrefix))
      pastedImageCounters.delete(key)
  }
}

// ---------------------------------------------------------------------------
// Pasted image counter (per agent, for naming)
// ---------------------------------------------------------------------------

export function nextPastedImageName(agentId: string): string {
  const count = (pastedImageCounters.get(agentId) ?? 0) + 1
  pastedImageCounters.set(agentId, count)
  return `Pasted Image ${count}`
}

// ---------------------------------------------------------------------------
// File reading
// ---------------------------------------------------------------------------

export function readFileAsAttachment(file: File, filename?: string): Promise<FileAttachment> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => {
      const data = new Uint8Array(reader.result as ArrayBuffer)
      const resolvedFilename = filename ?? file.name
      const details = inferAttachmentDetails(resolvedFilename, file.type, data)
      resolve({
        id: randomUUID(),
        file,
        filename: resolvedFilename,
        mimeType: details.mimeType,
        data,
        size: file.size,
      })
    }
    reader.onerror = () => reject(reader.error)
    reader.readAsArrayBuffer(file)
  })
}
