/** Attachment types, cache, and helpers for the agent editor panel. */

export interface FileAttachment {
  id: string
  file: File
  filename: string
  mimeType: string
  data: Uint8Array
  size: number
}

/** Maximum total size of all attachments (10 MB). */
export const MAX_TOTAL_ATTACHMENT_SIZE = 10 * 1024 * 1024

/** Supported MIME types (union of all provider support). */
export const SUPPORTED_MIME_TYPES = new Set([
  'image/png',
  'image/jpeg',
  'image/gif',
  'image/webp',
  'application/pdf',
])

/** File input accept attribute for supported types. */
export const ACCEPT_ATTRIBUTE = [...SUPPORTED_MIME_TYPES].join(',')

export function isSupportedMimeType(mime: string): boolean {
  return SUPPORTED_MIME_TYPES.has(mime)
}

export function isImageMimeType(mime: string): boolean {
  return mime.startsWith('image/')
}

export function totalAttachmentSize(attachments: FileAttachment[]): number {
  return attachments.reduce((sum, a) => sum + a.size, 0)
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
      resolve({
        id: crypto.randomUUID(),
        file,
        filename: filename ?? file.name,
        mimeType: file.type,
        data: new Uint8Array(reader.result as ArrayBuffer),
        size: file.size,
      })
    }
    reader.onerror = () => reject(reader.error)
    reader.readAsArrayBuffer(file)
  })
}
