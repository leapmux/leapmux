import type { MockModelDeliveredError, MockModelError } from './mockModelScript'
import { Buffer } from 'node:buffer'
import { concatBytes, encodeLengthDelimited, encodeStringField, encodeVarintField } from './cursorProtobuf'
import { connectEndOfStream } from './cursorWire'

/** Connect error codes used by the installed Cursor transport. */
function connectErrorCode(status: number): string {
  switch (status) {
    case 401: return 'unauthenticated'
    case 403: return 'permission_denied'
    case 404: return 'not_found'
    case 408:
    case 504: return 'deadline_exceeded'
    case 409: return 'aborted'
    case 429: return 'resource_exhausted'
    case 501: return 'unimplemented'
    case 502:
    case 503: return 'unavailable'
    default: return status >= 500
      ? 'internal'
      : 'invalid_argument'
  }
}

/**
 * The statuses that a gateway in front of Cursor's service answers when it cannot reach the service: a bad gateway,
 * an unavailable upstream, and a gateway timeout. The gateway's Connect error carries no `aiserver.v1.ErrorDetails`,
 * because only the service writes them.
 */
const GATEWAY_STATUSES: ReadonlySet<number> = new Set([502, 503, 504])

/**
 * End an actual Run with the native error of the HTTP status.
 *
 * A gateway status ends the Run with the gateway's bare Connect error. cursor-agent 2026.09.28 states that error as
 * `Error: RetriableError: [<code>] <message>`. Every other status ends the Run with the service's error details and
 * their explicit no-retry flag (`is_retryable` false), which cursor-agent states as `Error: NonRetriableError: ...`.
 */
export function cursorErrorResponse(error: MockModelError): { frame: Uint8Array, error: MockModelDeliveredError } {
  if (!Number.isInteger(error.status) || error.status < 400 || error.status > 599)
    throw new Error('The Cursor error requires an HTTP error status from 400 to 599.')
  if (typeof error.message !== 'string')
    throw new TypeError('The Cursor error requires a string message.')
  const deliveredError = {
    code: connectErrorCode(error.status),
    message: error.message,
  }
  if (GATEWAY_STATUSES.has(error.status)) {
    return {
      frame: connectEndOfStream({ error: deliveredError }),
      error: deliveredError,
    }
  }
  const quota = error.status === 429
  // ErrorDetails uses PRO_USER_USAGE_LIMIT=10 and CUSTOM_MESSAGE=29.
  const details = concatBytes([
    encodeVarintField(1, quota ? 10 : 29),
    encodeLengthDelimited(2, concatBytes([
      encodeStringField(1, quota ? 'Usage limit reached' : 'Model request failed'),
      encodeStringField(2, error.message),
      encodeVarintField(4, 0),
    ])),
    encodeVarintField(3, 1),
  ])
  return {
    frame: connectEndOfStream({
      error: {
        ...deliveredError,
        details: [
          {
            type: 'aiserver.v1.ErrorDetails',
            value: Buffer.from(details).toString('base64'),
          },
        ],
      },
    }),
    error: deliveredError,
  }
}
