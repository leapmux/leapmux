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
 * Keep the response open for the scripted delay.
 *
 * If the client disconnects before the delay ends, return false.
 * Callers then stop response generation.
 *
 * Listen to request.aborted and response.close.
 * The caller reads the body before this function runs.
 * IncomingMessage.close can therefore indicate a completed request body.
 * It cannot distinguish that state from a socket disconnect here.
 * ServerResponse.close signals the end of the HTTP exchange.
 */
export function holdOpen(request: IncomingMessage, response: ServerResponse, delayMs: number): Promise<boolean> {
  return new Promise((resolve) => {
    let settled = false
    let timer: ReturnType<typeof setTimeout>
    const onDisconnect = () => finish(false)
    function finish(delivered: boolean) {
      if (settled)
        return
      settled = true
      clearTimeout(timer)
      request.off('aborted', onDisconnect)
      response.off('close', onDisconnect)
      resolve(delivered)
    }
    timer = setTimeout(finish, delayMs, true)
    request.once('aborted', onDisconnect)
    response.once('close', onDisconnect)
  })
}
