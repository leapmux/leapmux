import type { IncomingMessage, OutgoingHttpHeaders, ServerResponse } from 'node:http'
import { Buffer } from 'node:buffer'

/** Limit encoded and decoded model requests to the same maximum size. */
export const MAX_MOCK_REQUEST_BYTES = 16 * 1024 * 1024

/** Keep delivered headers available through the public response API for native receipts. */
export function writeResponseHeaders(response: ServerResponse, status: number, headers: OutgoingHttpHeaders): void {
  for (const [name, value] of Object.entries(headers)) {
    if (value !== undefined)
      response.setHeader(name, value)
  }
  response.writeHead(status)
}

/** Read the complete request bytes and enforce the shared size limit. */
export async function readMockBody(request: IncomingMessage): Promise<Buffer> {
  const chunks: Buffer[] = []
  let size = 0
  for await (const chunk of request) {
    const bytes = Buffer.from(chunk)
    size += bytes.byteLength
    if (size > MAX_MOCK_REQUEST_BYTES)
      throw new Error(`The request body exceeds ${MAX_MOCK_REQUEST_BYTES} bytes.`)
    chunks.push(bytes)
  }
  return Buffer.concat(chunks)
}

/** Read a size-limited JSON request without changing its body shape. */
export async function readJSONBody(request: IncomingMessage): Promise<unknown> {
  const text = (await readMockBody(request)).toString('utf8')
  if (!text)
    throw new Error('The request body is empty.')
  try {
    return JSON.parse(text)
  }
  catch (error) {
    throw new Error('The request body is not valid JSON.', { cause: error })
  }
}

/** Write JSON and retain delivered headers for the existing response receipts. */
export function writeMockJSON(response: ServerResponse, status: number, value: unknown, headers: Record<string, string> = {}): void {
  writeResponseHeaders(response, status, { 'content-type': 'application/json', ...headers })
  response.end(JSON.stringify(value))
}

/**
 * The sources that can end a held answer early. A native request supplies its HTTP
 * exchange. A turn that a surface runs without an HTTP exchange of its own supplies
 * an abort signal.
 */
export interface DisconnectSignals {
  request?: IncomingMessage | undefined
  response?: ServerResponse | undefined
  signal?: AbortSignal | undefined
}

/**
 * Wait `delayMs` and return true, unless the client goes away first: then return false
 * at once, so the caller stops its answer.
 *
 * The request's `aborted`, the response's `close`, and the signal's `abort` each end the
 * wait. The request's `close` does not: the caller reads the body before it waits, and
 * IncomingMessage closes when its body ends, so that event cannot tell a completed body
 * from a socket that went away. ServerResponse.close signals the end of the exchange.
 */
export function waitUnlessDisconnected(delayMs: number, transport: DisconnectSignals): Promise<boolean> {
  const { request, response, signal } = transport
  if (request?.aborted || response?.destroyed || signal?.aborted)
    return Promise.resolve(false)
  if (delayMs <= 0)
    return Promise.resolve(true)
  return new Promise((resolve) => {
    let settled = false
    let timer: ReturnType<typeof setTimeout>
    const onDisconnect = () => finish(false)
    function finish(completed: boolean) {
      if (settled)
        return
      settled = true
      clearTimeout(timer)
      request?.off('aborted', onDisconnect)
      response?.off('close', onDisconnect)
      signal?.removeEventListener('abort', onDisconnect)
      resolve(completed)
    }
    timer = setTimeout(finish, delayMs, true)
    request?.once('aborted', onDisconnect)
    response?.once('close', onDisconnect)
    signal?.addEventListener('abort', onDisconnect, { once: true })
  })
}
