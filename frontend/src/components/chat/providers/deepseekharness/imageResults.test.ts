import { describe, expect, it } from 'vitest'
import { deepseekHarnessImageResults } from './imageResults'

function fixture() {
  const attachment = { attachmentId: `sha256:${'a'.repeat(64)}`, mediaType: 'image/png', bytes: 3, width: 2, height: 3 }
  const parentObject = { type: 'tool/result', data: { message: { toolCallId: 'image-call', content: [{ type: 'image', attachment }], isError: false } } }
  return {
    rawText: '',
    topLevel: parentObject,
    parentObject,
    wrapper: null,
    agentSessionId: 'native-session',
    supplementalContent: { imageAttachments: { sessionId: 'native-session', toolCallId: 'image-call', originalImages: [{ position: 0, attachment }], retainedImages: [{ position: 0, attachment }], images: { [attachment.attachmentId]: { attachment, data: 'AAEC' } } } },
  }
}

describe('deepseekHarnessImageResults', () => {
  it('adds native bytes to a separate display copy of the exact image reference', () => {
    const parsed = fixture()
    const before = JSON.stringify(parsed.parentObject)
    expect(deepseekHarnessImageResults(parsed)).toMatchObject({ data: { message: { content: [{ type: 'image', mimeType: 'image/png', data: 'AAEC', width: 2, height: 3 }] } } })
    expect(JSON.stringify(parsed.parentObject)).toBe(before)
  })

  it.each([{ field: 'sessionId', value: 'foreign' }, { field: 'toolCallId', value: 'another-call' }])('refuses a foreign $field', ({ field, value }) => {
    const parsed = fixture()
    expect(deepseekHarnessImageResults({ ...parsed, supplementalContent: { imageAttachments: { ...parsed.supplementalContent.imageAttachments, [field]: value } } })).toBeUndefined()
  })

  it('refuses bytes for a different image reference or malformed encoding', () => {
    const parsed = fixture()
    const id = parsed.parentObject.data.message.content[0]!.attachment.attachmentId
    const value = parsed.supplementalContent.imageAttachments.images[id]!
    expect(deepseekHarnessImageResults({ ...parsed, supplementalContent: { imageAttachments: { ...parsed.supplementalContent.imageAttachments, images: { [id]: { ...value, attachment: { ...value.attachment, width: 99 } } } } } })).toBeUndefined()
    value.data = 'invalid base64'
    expect(deepseekHarnessImageResults(parsed)).toBeUndefined()
  })
})

describe('image receipt order and native preview', () => {
  it('keeps the native preview when every original image is omitted from the native result', () => {
    const parsed = fixture()
    const attachment = parsed.parentObject.data.message.content[0]!.attachment
    const native = { ...parsed.parentObject, data: { message: { ...parsed.parentObject.data.message, content: [{ type: 'text', text: 'Native retained preview' }] } } }
    const imageAttachments = { ...parsed.supplementalContent.imageAttachments, originalImages: [{ position: 1, attachment }, { position: 3, attachment }], retainedImages: [] }
    const before = JSON.stringify(native)
    const result = deepseekHarnessImageResults({ ...parsed, parentObject: native, topLevel: native, supplementalContent: { imageAttachments } })
    expect(result).toMatchObject({ data: { message: { content: [{ type: 'text', text: 'Native retained preview' }, { type: 'image', data: 'AAEC' }, { type: 'image', data: 'AAEC' }] } } })
    expect(JSON.stringify(native)).toBe(before)
  })

  it('keeps repeated retained images and native fields around the original text', () => {
    const parsed = fixture()
    const image = { ...parsed.parentObject.data.message.content[0]!, nativeNote: 'Keep this native field' }
    const native = { ...parsed.parentObject, data: { message: { ...parsed.parentObject.data.message, content: [image, { type: 'text', text: 'Native retained text' }, image] } } }
    const imageAttachments = { ...parsed.supplementalContent.imageAttachments, originalImages: [{ position: 0, attachment: image.attachment }, { position: 2, attachment: image.attachment }], retainedImages: [{ position: 0, attachment: image.attachment }, { position: 2, attachment: image.attachment }] }
    expect(deepseekHarnessImageResults({ ...parsed, parentObject: native, supplementalContent: { imageAttachments } })).toMatchObject({ data: { message: { content: [{ type: 'image', data: 'AAEC', nativeNote: image.nativeNote }, { type: 'text', text: 'Native retained text' }, { type: 'image', data: 'AAEC', nativeNote: image.nativeNote }] } } })
  })

  it.each([undefined, null, -1, 0.5, Number.MAX_SAFE_INTEGER + 1])('refuses an absent or invalid image position: %j', (position) => {
    const parsed = fixture()
    const attachment = parsed.parentObject.data.message.content[0]!.attachment
    const imageAttachments = { ...parsed.supplementalContent.imageAttachments, originalImages: [{ position, attachment }] }
    expect(deepseekHarnessImageResults({ ...parsed, supplementalContent: { imageAttachments } })).toBeUndefined()
  })

  it('refuses duplicate positions and a retained snapshot that differs from the native image', () => {
    const parsed = fixture()
    const attachment = parsed.parentObject.data.message.content[0]!.attachment
    const original = parsed.supplementalContent.imageAttachments
    for (const imageAttachments of [
      { ...original, originalImages: [{ position: 0, attachment }, { position: 0, attachment }] },
      { ...original, retainedImages: [] },
      { ...original, retainedImages: [{ position: 1, attachment }] },
      { ...original, retainedImages: [{ position: 0, attachment: { ...attachment, width: 99 } }] },
    ]) {
      expect(deepseekHarnessImageResults({ ...parsed, supplementalContent: { imageAttachments } })).toBeUndefined()
    }
  })
})
