import { fromBinary } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { decodeProxyResponse } from '~/api/platformBridge'
import { RelayReadResponseSchema } from '../../../src/generated/proto/leapmux/desktop/v1/frame_pb'
import { encodeDesktopProxyResponse, encodeDesktopRelayRead } from './desktopProxyMock'

describe('encodeDesktopProxyResponse', () => {
  it('returns raw ProxyHttpResponse protobuf bytes', () => {
    const body = new TextEncoder().encode('hub response')

    const decoded = decodeProxyResponse(encodeDesktopProxyResponse({
      status: 200,
      headers: {
        'content-type': [
          'application/proto',
        ],
      },
      body: body.buffer,
    }))

    expect(decoded.status).toBe(200)
    expect(decoded.headers).toEqual({
      'content-type': [
        'application/proto',
      ],
    })
    expect([...decoded.body]).toEqual([...body])
  })
})

describe('encodeDesktopRelayRead', () => {
  it('returns an acknowledged relay item with its cursor', () => {
    const decoded = fromBinary(
      RelayReadResponseSchema,
      new Uint8Array(encodeDesktopRelayRead(17n, {
        data: new Uint8Array([1, 2, 3]),
      })),
    )

    expect(decoded.cursor).toBe(17n)
    expect(decoded.item.case).toBe('data')
    expect(decoded.item.value).toEqual(new Uint8Array([1, 2, 3]))
  })

  it('preserves an acknowledged close item', () => {
    const decoded = fromBinary(
      RelayReadResponseSchema,
      new Uint8Array(encodeDesktopRelayRead(18n, {
        close: {
          code: 1006,
          reason: 'relay overflow',
          wasClean: false,
        },
      })),
    )

    expect(decoded.cursor).toBe(18n)
    expect(decoded.item.case).toBe('close')
    expect(decoded.item.value).toEqual(expect.objectContaining({
      code: 1006,
      reason: 'relay overflow',
      wasClean: false,
    }))
  })
})
