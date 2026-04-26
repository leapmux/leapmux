import type { Accessor } from 'solid-js'
import type { FileAttachment, PendingAttachmentFile } from './attachments'
import type { AttachmentCapabilities } from './providers/registry'
import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { createEffect, createMemo, createSignal, on } from 'solid-js'
import { showWarnToast } from '~/components/common/Toast'
import {
  buildAcceptAttribute,
  clearAttachments,
  collectDroppedAttachmentFiles,
  describeUnsupportedAttachment,
  getAttachments,
  inferAttachmentDetails,
  isAttachmentSupported,
  MAX_TOTAL_ATTACHMENT_SIZE,
  nextPastedImageName,
  readFileAsAttachment,
  setAttachments as setAttachmentsCache,
  totalAttachmentSize,
} from './attachments'
import { getProviderPlugin } from './providers/registry'

export interface UseChatAttachmentsOptions {
  agentId: Accessor<string>
  agentProvider: Accessor<AgentProvider>
  /** Display label for the current provider, used in attachment-rejection toasts. */
  providerLabel: Accessor<string>
}

export interface UseChatAttachmentsResult {
  attachments: Accessor<FileAttachment[]>
  capabilities: Accessor<AttachmentCapabilities | undefined>
  acceptAttribute: Accessor<string | undefined>
  addFiles: (files: FileList | File[] | PendingAttachmentFile[], isPastedImage?: boolean) => Promise<number>
  removeAttachment: (id: string) => void
  clearAllAttachments: () => void
  /** Read files from a file input and add them. Pass the input element to also reset its value afterwards. */
  handleFileInputChange: (input: HTMLInputElement | undefined) => void
  /** Walk a DataTransfer (drop event) and add valid files; surfaces oversize toast. */
  addDroppedDataTransfer: (dataTransfer: DataTransfer) => Promise<number>
}

/**
 * Manages chat-message attachments for a single agent: per-agent storage cache,
 * size/kind validation against the provider's capabilities, and helpers for
 * file-input/drop/paste pathways.
 */
export function useChatAttachments(opts: UseChatAttachmentsOptions): UseChatAttachmentsResult {
  const [attachments, setAttachments] = createSignal<FileAttachment[]>([])

  // Swap attachments on agentId change (mirrors the editor height cache pattern).
  createEffect(on(opts.agentId, (agentId, prevAgentId) => {
    if (prevAgentId) {
      setAttachmentsCache(prevAgentId, attachments())
    }
    setAttachments(agentId ? getAttachments(agentId) : [])
  }))

  const capabilities = createMemo(() => getProviderPlugin(opts.agentProvider())?.attachments)
  const acceptAttribute = createMemo(() => buildAcceptAttribute(capabilities()))

  const addFiles = async (files: FileList | File[] | PendingAttachmentFile[], isPastedImage?: boolean): Promise<number> => {
    const currentAttachments = attachments()
    let currentSize = totalAttachmentSize(currentAttachments)

    const accepted: FileAttachment[] = []
    const rejectionReasons = new Map<string, number>()
    let sizeLimitHit = false
    // Sequential reads to avoid I/O burst; parallelism isn't needed given the 10 MB cap.
    for (const item of [...files]) {
      const file = item instanceof File ? item : item.file
      if (currentSize + file.size > MAX_TOTAL_ATTACHMENT_SIZE) {
        sizeLimitHit = true
        break
      }
      const filename = isPastedImage
        ? `${nextPastedImageName(opts.agentId())}.${file.type.split('/')[1] || 'png'}`
        : (item instanceof File ? undefined : item.filename)
      const attachment = await readFileAsAttachment(file, filename)
      const details = inferAttachmentDetails(attachment.filename, attachment.mimeType, attachment.data)
      if (!isAttachmentSupported(details.kind, capabilities())) {
        const reason = describeUnsupportedAttachment(details.kind, opts.providerLabel())
        rejectionReasons.set(reason, (rejectionReasons.get(reason) ?? 0) + 1)
        continue
      }
      accepted.push(attachment)
      currentSize += file.size
    }

    for (const [reason, count] of rejectionReasons) {
      showWarnToast(count === 1 ? reason : `${reason} (${count} files)`)
    }
    if (sizeLimitHit)
      showWarnToast('Total attachment size exceeds 10 MB')

    if (accepted.length === 0)
      return 0

    const updated = [...currentAttachments, ...accepted]
    setAttachments(updated)
    const id = opts.agentId()
    if (id)
      setAttachmentsCache(id, updated)
    return accepted.length
  }

  const removeAttachment = (id: string) => {
    const updated = attachments().filter(a => a.id !== id)
    setAttachments(updated)
    const aid = opts.agentId()
    if (aid)
      setAttachmentsCache(aid, updated)
  }

  const clearAllAttachments = () => {
    setAttachments([])
    const id = opts.agentId()
    if (id)
      clearAttachments(id)
  }

  const handleFileInputChange = (input: HTMLInputElement | undefined) => {
    if (input?.files?.length) {
      addFiles(input.files)
      input.value = ''
    }
  }

  const addDroppedDataTransfer = async (dataTransfer: DataTransfer): Promise<number> => {
    const { files, sizeLimitHit } = await collectDroppedAttachmentFiles(dataTransfer, totalAttachmentSize(attachments()))
    if (sizeLimitHit)
      showWarnToast('Total attachment size exceeds 10 MB')
    return addFiles(files)
  }

  return {
    attachments,
    capabilities,
    acceptAttribute,
    addFiles,
    removeAttachment,
    clearAllAttachments,
    handleFileInputChange,
    addDroppedDataTransfer,
  }
}
