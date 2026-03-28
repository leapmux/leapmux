/** Attachment types, cache, and helpers for the agent editor panel. */

import type { AttachmentCapabilities } from './providers/registry'

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

export type AttachmentKind = 'text' | 'image' | 'pdf' | 'binary'

export interface AttachmentDetails {
  kind: AttachmentKind
  mimeType: string
}

/** Maximum total size of all attachments (10 MB). */
export const MAX_TOTAL_ATTACHMENT_SIZE = 10 * 1024 * 1024

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

const SUPPORTED_IMAGE_MIME_TYPES = new Set([
  'image/png',
  'image/jpeg',
  'image/gif',
  'image/webp',
])

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
  if (capabilities.binary)
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
    accepted.add('application/pdf')

  return accepted.size > 0 ? [...accepted].join(',') : undefined
}

function extensionOf(filename: string): string {
  const trimmed = filename.trim().toLowerCase()
  if (!trimmed)
    return ''
  const lastDot = trimmed.lastIndexOf('.')
  if (lastDot <= 0 || lastDot === trimmed.length - 1)
    return trimmed === 'dockerfile' ? 'dockerfile' : ''
  return trimmed.slice(lastDot + 1)
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
  return TEXT_MIME_BY_EXTENSION.get(extensionOf(filename)) ?? ''
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
  const utf8 = isUtf8Text(data)
  const effectiveMime = !GENERIC_BINARY_MIME_TYPES.has(normalizedMime)
    ? normalizedMime
    : (inferredMime || (utf8 ? 'text/plain' : 'application/octet-stream'))

  if (SUPPORTED_IMAGE_MIME_TYPES.has(effectiveMime))
    return { kind: 'image', mimeType: effectiveMime }

  if (effectiveMime === 'application/pdf')
    return { kind: 'pdf', mimeType: effectiveMime }

  if (isTextualMimeType(effectiveMime) && utf8)
    return { kind: 'text', mimeType: effectiveMime }

  if (GENERIC_BINARY_MIME_TYPES.has(normalizedMime) && utf8)
    return { kind: 'text', mimeType: inferredMime || 'text/plain' }

  return { kind: 'binary', mimeType: effectiveMime || 'application/octet-stream' }
}

export function isAttachmentSupported(kind: AttachmentKind, capabilities: AttachmentCapabilities | undefined): boolean {
  if (!capabilities)
    return true
  switch (kind) {
    case 'text': return capabilities.text
    case 'image': return capabilities.image
    case 'pdf': return capabilities.pdf
    case 'binary': return capabilities.binary
  }
}

export function describeUnsupportedAttachment(kind: AttachmentKind, providerLabel: string): string {
  switch (kind) {
    case 'pdf':
      return `${providerLabel} does not support PDF attachments`
    case 'binary':
      return `${providerLabel} does not support binary attachments`
    case 'image':
      return `${providerLabel} does not support image attachments`
    case 'text':
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

async function readAllDirectoryEntries(entry: FileSystemDirectoryEntry): Promise<FileSystemEntry[]> {
  const reader = entry.createReader()
  const entries: FileSystemEntry[] = []
  while (true) {
    const batch = await new Promise<FileSystemEntry[]>((resolve, reject) => {
      reader.readEntries(resolve, reject)
    })
    if (batch.length === 0)
      return entries
    entries.push(...batch)
  }
}

function sortedEntries(entries: FileSystemEntry[]): FileSystemEntry[] {
  return entries.toSorted((a, b) => a.name.localeCompare(b.name))
}

async function collectFilesFromEntry(
  entry: FileSystemEntry,
  currentSizeRef: { value: number },
  results: PendingAttachmentFile[],
): Promise<boolean> {
  if (entry.isFile) {
    const file = await fileFromEntry(entry as FileSystemFileEntry)
    if (currentSizeRef.value + file.size > MAX_TOTAL_ATTACHMENT_SIZE)
      return true
    currentSizeRef.value += file.size
    results.push({
      file,
      filename: entry.fullPath.replace(LEADING_SLASHES, ''),
    })
    return false
  }

  if (!entry.isDirectory)
    return false

  const childEntries = sortedEntries(await readAllDirectoryEntries(entry as FileSystemDirectoryEntry))
  for (const childEntry of childEntries) {
    if (await collectFilesFromEntry(childEntry, currentSizeRef, results))
      return true
  }
  return false
}

/**
 * Collect dropped files, recursively expanding directories when supported by
 * the browser/webview. Traversal stops immediately once the next file would
 * exceed the global attachment size limit.
 */
export async function collectDroppedAttachmentFiles(
  dataTransfer: DataTransfer,
  currentSize = 0,
): Promise<{ files: PendingAttachmentFile[], sizeLimitHit: boolean }> {
  const results: PendingAttachmentFile[] = []
  const currentSizeRef = { value: currentSize }
  const items = [...(dataTransfer.items ?? [])] as WebkitDataTransferItem[]
  const entries = items
    .map(item => item.webkitGetAsEntry?.() ?? null)
    .filter((entry): entry is NonNullable<typeof entry> => entry !== null) as FileSystemEntry[]

  if (entries.length > 0) {
    for (const entry of sortedEntries(entries)) {
      if (await collectFilesFromEntry(entry, currentSizeRef, results))
        return { files: results, sizeLimitHit: true }
    }
    return { files: results, sizeLimitHit: false }
  }

  for (const file of [...dataTransfer.files]) {
    if (currentSizeRef.value + file.size > MAX_TOTAL_ATTACHMENT_SIZE)
      return { files: results, sizeLimitHit: true }
    currentSizeRef.value += file.size
    results.push({ file })
  }
  return { files: results, sizeLimitHit: false }
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
        id: crypto.randomUUID(),
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
