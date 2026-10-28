import type { Accessor } from 'solid-js'
import type { FileAttachment, PendingAttachmentFile } from './attachments'
import type { AttachmentCapabilities } from './providers/registry'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { createEffect, createMemo, createSignal, on, onCleanup } from 'solid-js'
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
import { providerFor } from './providers/registry'

export interface UseChatAttachmentsOptions {
  agentId: Accessor<string>
  agentProvider: Accessor<AgentProvider>
  /** Display label for the current provider, used in attachment-rejection toasts. */
  providerLabel: Accessor<string>
}

export interface UseChatAttachmentsResult {
  attachments: Accessor<FileAttachment[]>
  activeDraftKey: Accessor<string>
  capabilities: Accessor<AttachmentCapabilities | undefined>
  acceptAttribute: Accessor<string | undefined>
  addFiles: (files: FileList | File[] | PendingAttachmentFile[], isPastedImage?: boolean) => Promise<number>
  removeAttachment: (id: string) => void
  clearAllAttachments: () => void
  replaceAttachments: (attachments: FileAttachment[]) => void
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
  const [activeDraftKey, setActiveDraftKey] = createSignal('')
  let draftLifetime = {}
  let disposed = false

  const invalidateReads = () => {
    draftLifetime = {}
  }
  onCleanup(() => {
    disposed = true
    invalidateReads()
  })

  // Each mutation updates the cache. A draft switch only loads its next owner.
  createEffect(on(opts.agentId, (agentId, previousAgentId) => {
    if (agentId === previousAgentId)
      return
    invalidateReads()
    setAttachments(agentId ? getAttachments(agentId) : [])
    setActiveDraftKey(agentId)
  }))

  const capabilities = createMemo(() => providerFor(opts.agentProvider())?.configuration?.attachments)
  const acceptAttribute = createMemo(() => buildAcceptAttribute(capabilities()))

  const captureDraft = () => ({
    key: opts.agentId(),
    provider: opts.agentProvider(),
    lifetime: draftLifetime,
    capabilities: capabilities(),
    providerLabel: opts.providerLabel(),
  })
  const ownsDraft = (draft: ReturnType<typeof captureDraft>) => !disposed && draft.key !== ''
    && draft.lifetime === draftLifetime && draft.key === opts.agentId() && draft.provider === opts.agentProvider()

  const writeAttachments = (next: FileAttachment[], draft: ReturnType<typeof captureDraft>): boolean => {
    if (!ownsDraft(draft))
      return false
    setAttachmentsCache(draft.key, next)
    setAttachments(next)
    return true
  }

  const readFiles = async (
    files: FileList | File[] | PendingAttachmentFile[],
    draft: ReturnType<typeof captureDraft>,
    isPastedImage?: boolean,
  ): Promise<number> => {
    if (!ownsDraft(draft))
      return 0
    let currentSize = totalAttachmentSize(attachments())

    const accepted: FileAttachment[] = []
    const rejectionReasons = new Map<string, number>()
    let sizeLimitHit = false
    // Sequential reads to avoid I/O burst; parallelism isn't needed given the 10 MB cap.
    for (const item of [...files]) {
      if (!ownsDraft(draft))
        return 0
      const file = item instanceof File ? item : item.file
      if (currentSize + file.size > MAX_TOTAL_ATTACHMENT_SIZE) {
        sizeLimitHit = true
        break
      }
      const filename = isPastedImage
        ? `${nextPastedImageName(draft.key)}.${file.type.split('/')[1] || 'png'}`
        : (item instanceof File ? undefined : item.filename)
      let attachment: FileAttachment
      try {
        attachment = await readFileAsAttachment(file, filename)
      }
      catch {
        if (!ownsDraft(draft))
          return 0
        const reason = `Could not read attachment "${filename ?? file.name}"`
        rejectionReasons.set(reason, (rejectionReasons.get(reason) ?? 0) + 1)
        continue
      }
      if (!ownsDraft(draft))
        return 0
      const details = inferAttachmentDetails(attachment.filename, attachment.mimeType, attachment.data)
      if (!isAttachmentSupported(details.kind, draft.capabilities, details.mimeType)) {
        const reason = describeUnsupportedAttachment(details.kind, draft.providerLabel, draft.capabilities)
        rejectionReasons.set(reason, (rejectionReasons.get(reason) ?? 0) + 1)
        continue
      }
      accepted.push(attachment)
      currentSize += file.size
    }

    if (!ownsDraft(draft))
      return 0
    // Another selection or removal can finish while these files are read.
    // Commit against the current draft and check its aggregate size again.
    const currentAttachments = attachments()
    currentSize = totalAttachmentSize(currentAttachments)
    const additions: FileAttachment[] = []
    for (const attachment of accepted) {
      if (currentSize + attachment.size > MAX_TOTAL_ATTACHMENT_SIZE) {
        sizeLimitHit = true
        break
      }
      additions.push(attachment)
      currentSize += attachment.size
    }
    if (additions.length > 0 && !writeAttachments([...currentAttachments, ...additions], draft))
      return 0

    for (const [reason, count] of rejectionReasons) {
      showWarnToast(count === 1 ? reason : `${reason} (${count} files)`)
    }
    if (sizeLimitHit)
      showWarnToast(`One message accepts at most ${MAX_TOTAL_ATTACHMENT_SIZE / 1024 / 1024} MiB of text and attachments`)

    return additions.length
  }

  const addFiles = (files: FileList | File[] | PendingAttachmentFile[], isPastedImage?: boolean) => readFiles(files, captureDraft(), isPastedImage)

  const removeAttachment = (id: string) => {
    const updated = attachments().filter(a => a.id !== id)
    writeAttachments(updated, captureDraft())
  }

  const clearAllAttachments = () => {
    if (disposed)
      return
    invalidateReads()
    const id = opts.agentId()
    if (id)
      clearAttachments(id)
    setAttachments([])
  }

  const replaceAttachments = (next: FileAttachment[]) => {
    invalidateReads()
    writeAttachments(next, captureDraft())
  }

  const handleFileInputChange = (input: HTMLInputElement | undefined) => {
    if (input?.files?.length) {
      addFiles(input.files)
      input.value = ''
    }
  }

  const addDroppedDataTransfer = async (dataTransfer: DataTransfer): Promise<number> => {
    const draft = captureDraft()
    if (!ownsDraft(draft))
      return 0
    let collected: Awaited<ReturnType<typeof collectDroppedAttachmentFiles>>
    try {
      collected = await collectDroppedAttachmentFiles(dataTransfer, totalAttachmentSize(attachments()))
    }
    catch {
      if (ownsDraft(draft))
        showWarnToast('Could not read the dropped files')
      return 0
    }
    if (!ownsDraft(draft))
      return 0
    const { files, sizeLimitHit, failures } = collected
    if (failures.length > 0) {
      const paths = failures.flatMap(failure => failure.path === undefined ? [] : [failure.path])
      showWarnToast(`Could not read the dropped files${paths.length > 0 ? `: ${paths.join(', ')}` : ''}`)
    }
    if (sizeLimitHit)
      showWarnToast(`One message accepts at most ${MAX_TOTAL_ATTACHMENT_SIZE / 1024 / 1024} MiB of text and attachments`)
    return readFiles(files, draft)
  }

  return {
    attachments,
    activeDraftKey,
    capabilities,
    acceptAttribute,
    addFiles,
    removeAttachment,
    clearAllAttachments,
    replaceAttachments,
    handleFileInputChange,
    addDroppedDataTransfer,
  }
}
