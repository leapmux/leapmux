import { create, toBinary } from '@bufbuild/protobuf'
import { HeaderValuesSchema, ProxyHttpResponseSchema, RelayCloseSchema, RelayReadResponseSchema } from '../../../src/generated/proto/leapmux/desktop/v1/frame_pb'

export function encodeDesktopProxyResponse(response: {
  status: number
  headers: Record<string, string[]>
  body: ArrayBuffer | Uint8Array
}): ArrayBuffer {
  const bytes = toBinary(ProxyHttpResponseSchema, create(ProxyHttpResponseSchema, {
    status: response.status,
    headers: Object.fromEntries(
      Object.entries(response.headers).map(([name, values]) => [
        name,
        create(HeaderValuesSchema, { values }),
      ]),
    ),
    body: response.body instanceof Uint8Array
      ? response.body
      : new Uint8Array(response.body),
  }))
  return bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength) as ArrayBuffer
}

export function encodeDesktopRelayRead(
  cursor: bigint,
  item: { data: Uint8Array } | { close: { code: number, reason: string, wasClean: boolean } },
): ArrayBuffer {
  const value = 'data' in item
    ? { case: 'data' as const, value: item.data }
    : {
        case: 'close' as const,
        value: create(RelayCloseSchema, item.close),
      }
  const bytes = toBinary(RelayReadResponseSchema, create(RelayReadResponseSchema, {
    cursor,
    item: value,
  }))
  return bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength) as ArrayBuffer
}
