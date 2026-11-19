import { base64ToUint8Array, uint8ArrayToBase64 } from '~/lib/base64'

/** Validate native inline bytes before exposing them as a browser image. */
export function kimiInlineImage(mimeType: unknown, data: unknown): { mimeType: string, data: string } | null {
  if (typeof mimeType !== 'string' || !/^image\/[a-z0-9!#$%&'*+.^_`|~-]+$/u.test(mimeType)
    || typeof data !== 'string' || !data || !/^(?:[A-Z0-9+/]{4})*(?:[A-Z0-9+/]{2}==|[A-Z0-9+/]{3}=)?$/iu.test(data)) {
    return null
  }
  return uint8ArrayToBase64(base64ToUint8Array(data)) === data
    ? { mimeType, data }
    : null
}
