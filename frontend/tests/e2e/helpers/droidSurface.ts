import type { IncomingMessage, ServerResponse } from 'node:http'
import { writeMockJSON } from './mockHttp'

export interface DroidSurfaceOptions {
  modelKey: string
}

/** Serve Droid's native identity request without selecting a model answer. */
export function handleDroidHttp(request: IncomingMessage, response: ServerResponse, url: URL, options: DroidSurfaceOptions): boolean {
  if (request.method !== 'GET' || url.pathname !== '/v1/api/cli/whoami')
    return false
  if (request.headers.authorization !== `Bearer ${options.modelKey}`)
    writeMockJSON(response, 401, { error: { message: 'The identity request requires the isolated Droid model key.' } })
  else
    writeMockJSON(response, 200, { userId: 'leapmux-e2e-user', orgId: 'leapmux-e2e-org' })
  return true
}
