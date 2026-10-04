import type { OutgoingHttpHeaders, ServerResponse } from 'node:http'

/** Keep delivered headers available through the public response API for native receipts. */
export function writeResponseHeaders(response: ServerResponse, status: number, headers: OutgoingHttpHeaders): void {
  for (const [name, value] of Object.entries(headers)) {
    if (value !== undefined)
      response.setHeader(name, value)
  }
  response.writeHead(status)
}
