import { timingSafeEqual } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { createServer } from 'node:http'
import { join } from 'node:path'

function nativeLocalOnly() {
  if (process.argv.includes('--local-only') || process.env.CMD_LOCAL_ONLY === '1' || process.env.CMD_LOCAL_ONLY === 'true')
    return true
  const home = process.env.HOME ?? process.env.USERPROFILE
  if (!home)
    return false
  const mode = process.argv.includes('--local') ? 'local' : process.argv.includes('--staging') ? 'staging' : process.env.COMMANDCODE_API_ENV
  const config = mode === 'local' ? 'config.local.json' : mode === 'staging' ? 'config.staging.json' : 'config.json'
  try {
    return JSON.parse(readFileSync(join(home, '.commandcode', config), 'utf8'))?.localOnly === true
  }
  catch {
    return false
  }
}

/** Expose native session compaction through the official Command Code mod API. */
export default async function commandCodeBridge(cmd) {
  const secret = process.env.LEAPMUX_COMMANDCODE_BRIDGE_SECRET
  if (typeof secret !== 'string' || !/^[a-f0-9]{64}$/.test(secret))
    throw new Error('The Command Code bridge secret is absent or invalid.')

  let active = false
  let compacting = false
  const credential = Buffer.from(`Bearer ${secret}`, 'utf8')
  const authorized = request => {
    const value = request.headers.authorization
    if (typeof value !== 'string')
      return false
    const received = Buffer.from(value, 'utf8')
    return received.length === credential.length && timingSafeEqual(received, credential)
  }
  const reply = (response, status, data) => {
    if (response.destroyed)
      return
    response.writeHead(status, { 'content-type': 'application/json', 'cache-control': 'no-store' })
    response.end(JSON.stringify(data))
  }
  const server = createServer((request, response) => {
    request.resume()
    if (!authorized(request)) {
      reply(response, 401, { error: 'The Command Code bridge credential is invalid.' })
      return
    }
    if (request.method !== 'POST' || request.url !== '/compact') {
      reply(response, 404, { error: 'The Command Code bridge operation is unknown.' })
      return
    }
    if (!active || compacting) {
      reply(response, 409, { error: active ? 'Native compaction already runs.' : 'The native session is not active.' })
      return
    }
    compacting = true
    Promise.resolve().then(() => cmd.sessions.compact()).then(
      () => reply(response, 200, { completed: true }),
      error => reply(response, 500, { error: error instanceof Error ? error.message : String(error) }),
    ).finally(() => { compacting = false })
  })
  server.requestTimeout = 30_000
  server.headersTimeout = 10_000
  server.maxHeadersCount = 16
  server.maxConnections = 4
  cmd.on('session_start', () => { active = true })
  cmd.on('session_shutdown', () => {
    active = false
    server.close()
    server.closeAllConnections()
  })
  await new Promise((resolve, reject) => {
    const failed = error => { server.close(); reject(error) }
    server.once('error', failed)
    server.listen(0, '127.0.0.1', () => {
      server.removeListener('error', failed)
      resolve()
    })
  })
  server.on('error', error => {
    active = false
    process.stderr.write(`The Command Code bridge failed: ${error.message}\n`)
    server.closeAllConnections()
    server.close()
  })
  const address = server.address()
  if (address === null || typeof address === 'string') {
    server.close()
    throw new Error('The Command Code bridge has no TCP address.')
  }
  server.unref()
  process.stdout.write(`${JSON.stringify({ type: 'leapmux_commandcode_bridge', port: address.port, localOnly: nativeLocalOnly() })}\n`)
}
