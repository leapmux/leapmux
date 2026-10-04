import type { ParsedMessageContent } from '~/lib/messageParser'
import { DEEPSEEK_HARNESS_EVENT, DEEPSEEK_HARNESS_IMAGE_POSITION_FIELD, DEEPSEEK_HARNESS_IMAGE_RECEIPT_FIELD, DEEPSEEK_HARNESS_IMAGE_REFERENCE_FIELD, DEEPSEEK_HARNESS_IMAGE_VALUE_FIELD, DEEPSEEK_HARNESS_SUPPLEMENT } from '~/generated/contracts/deepseek-harness-protocol'
import { base64ToUint8Array, uint8ArrayToBase64 } from '~/lib/base64'
import { isObject, pickObject } from '~/lib/jsonPick'
import { deepseekHarnessEventData } from './protocol'

interface ImagePosition {
  position: number
  attachment: Record<string, unknown>
}

const REFERENCE_FIELDS = Object.values(DEEPSEEK_HARNESS_IMAGE_REFERENCE_FIELD)

function sameReference(first: Record<string, unknown>, second: Record<string, unknown>): boolean {
  return REFERENCE_FIELDS.every(field => first[field] === second[field])
}

function imagePositions(value: unknown): ImagePosition[] | undefined {
  if (!Array.isArray(value))
    return undefined
  const positions: ImagePosition[] = []
  let previous = -1
  for (const item of value) {
    const position = isObject(item) ? item[DEEPSEEK_HARNESS_IMAGE_POSITION_FIELD.Position] : undefined
    const attachment = pickObject(isObject(item) ? item : undefined, DEEPSEEK_HARNESS_IMAGE_POSITION_FIELD.Attachment)
    if (!attachment || typeof position !== 'number' || !Number.isSafeInteger(position) || position <= previous)
      return undefined
    previous = position
    positions.push({ position, attachment })
  }
  return positions
}

function displayImage(reference: Record<string, unknown>, images: Record<string, unknown>): Record<string, unknown> | undefined {
  const id = reference[DEEPSEEK_HARNESS_IMAGE_REFERENCE_FIELD.AttachmentID]
  const bytes = reference[DEEPSEEK_HARNESS_IMAGE_REFERENCE_FIELD.Bytes]
  const width = reference[DEEPSEEK_HARNESS_IMAGE_REFERENCE_FIELD.Width]
  const height = reference[DEEPSEEK_HARNESS_IMAGE_REFERENCE_FIELD.Height]
  const mimeType = reference[DEEPSEEK_HARNESS_IMAGE_REFERENCE_FIELD.MediaType]
  const value = typeof id === 'string' && Object.hasOwn(images, id) ? pickObject(images, id) : undefined
  const attachment = pickObject(value, DEEPSEEK_HARNESS_IMAGE_VALUE_FIELD.Attachment)
  const data = value?.[DEEPSEEK_HARNESS_IMAGE_VALUE_FIELD.Data]
  if (!attachment || !sameReference(reference, attachment) || typeof id !== 'string' || !/^sha256:[0-9a-f]{64}$/u.test(id)
    || typeof mimeType !== 'string' || !mimeType || typeof bytes !== 'number' || !Number.isSafeInteger(bytes) || bytes <= 0
    || typeof width !== 'number' || !Number.isSafeInteger(width) || width <= 0
    || typeof height !== 'number' || !Number.isSafeInteger(height) || height <= 0
    || typeof data !== 'string' || !/^(?:[A-Z0-9+/]{4})*(?:[A-Z0-9+/]{2}==|[A-Z0-9+/]{3}=)?$/iu.test(data) || !data) {
    return undefined
  }
  const decoded = base64ToUint8Array(data)
  if (decoded.byteLength !== bytes || uint8ArrayToBase64(decoded) !== data)
    return undefined
  return { type: 'image', attachment: reference, mimeType, data, width, height }
}

/** Keep the native preview and restore only authenticated image occurrences. */
export function deepseekHarnessImageResults(parsed: ParsedMessageContent): Record<string, unknown> | undefined {
  const payload = parsed.parentObject
  const data = deepseekHarnessEventData(payload, DEEPSEEK_HARNESS_EVENT.ToolResult)
  const message = pickObject(data, 'message')
  const receipt = pickObject(isObject(parsed.supplementalContent) ? parsed.supplementalContent : undefined, DEEPSEEK_HARNESS_SUPPLEMENT.ImageAttachments)
  const images = pickObject(receipt, DEEPSEEK_HARNESS_IMAGE_RECEIPT_FIELD.Images)
  const originals = imagePositions(receipt?.[DEEPSEEK_HARNESS_IMAGE_RECEIPT_FIELD.OriginalImages])
  const retained = imagePositions(receipt?.[DEEPSEEK_HARNESS_IMAGE_RECEIPT_FIELD.RetainedImages])
  if (!payload || !data || !message || !images || !originals?.length || !retained || !Array.isArray(message.content)
    || !parsed.agentSessionId || receipt?.[DEEPSEEK_HARNESS_IMAGE_RECEIPT_FIELD.SessionID] !== parsed.agentSessionId
    || receipt[DEEPSEEK_HARNESS_IMAGE_RECEIPT_FIELD.ToolCallID] !== message.toolCallId) {
    return undefined
  }
  const displays: Record<string, unknown>[] = []
  const decoded = new Map<string, Record<string, unknown>>()
  for (const original of originals) {
    const id = original.attachment[DEEPSEEK_HARNESS_IMAGE_REFERENCE_FIELD.AttachmentID]
    if (typeof id !== 'string')
      return undefined
    const cached = decoded.get(id)
    if (cached && !sameReference(pickObject(cached, 'attachment')!, original.attachment))
      return undefined
    const image = cached ?? displayImage(original.attachment, images)
    if (!image)
      return undefined
    decoded.set(id, image)
    displays.push(image)
  }
  const nativeImages = message.content.flatMap((block, position) => {
    const attachment = isObject(block) && block.type === 'image' ? pickObject(block, 'attachment') : null
    return attachment ? [{ position, attachment }] : []
  })
  if (nativeImages.length !== retained.length || retained.some((image, index) => image.position !== nativeImages[index]?.position
    || !sameReference(image.attachment, nativeImages[index]!.attachment))) {
    return undefined
  }
  const content: unknown[] = []
  let next = 0
  for (const block of message.content) {
    const attachment = isObject(block) && block.type === 'image' ? pickObject(block, 'attachment') : null
    if (!attachment) {
      content.push(block)
      continue
    }
    let match = next
    while (match < originals.length && !sameReference(originals[match]!.attachment, attachment))
      match++
    if (match === originals.length)
      return undefined
    while (next < match)
      content.push(displays[next++])
    content.push({ ...block, ...displays[next++], attachment })
  }
  while (next < displays.length)
    content.push(displays[next++])
  return { ...payload, data: { ...data, message: { ...message, content } } }
}
