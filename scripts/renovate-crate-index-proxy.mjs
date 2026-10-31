import { spawn } from 'node:child_process'
import process from 'node:process'
import { createInterface } from 'node:readline'
import { fileURLToPath } from 'node:url'
import { registryFetch } from './renovate-registry.mjs'
import { isPublishedBy } from './renovate-release-age.mjs'

// cargo resolves each crate with no release age, and its unstable publish-time option
// exists only on `cargo generate-lockfile`. This proxy serves the crates.io sparse index to
// cargo without the versions that the release age holds back, so a resolution never
// selects one. cargo replaces the source only to resolve, so Cargo.lock keeps the crates.io
// source and its checksums.

const SOURCE = 'leapmux-aged-index'

/** Return the cargo options that resolve crates.io through the aged index at `url`. */
export function agedIndexArgs(url) {
  return [
    '--config',
    `source.crates-io.replace-with="${SOURCE}"`,
    '--config',
    `source.${SOURCE}.registry="${url}"`,
  ]
}

/**
 * Return the lines of a sparse index file that the release age admits: each version that
 * was published by `cutoffMs`, and each `name@version` of `allowed`. A line without a
 * publication time is young. A line that is not an index entry fails the read.
 */
export function filterIndexFile(text, { cutoffMs, allowed }) {
  const kept = text.split('\n').filter((line) => {
    if (line.trim() === '')
      return false
    const entry = JSON.parse(line)
    if (typeof entry?.name !== 'string' || typeof entry.vers !== 'string')
      throw new TypeError('A crate index line must give the name and the version.')
    return allowed.has(`${entry.name}@${entry.vers}`) || isPublishedBy(Date.parse(entry.pubtime), cutoffMs)
  })
  return kept.map(line => `${line}\n`).join('')
}

// The index configuration, or the file of one lowercase crate name under its directory.
const INDEX_PATH = /^\/(?:config\.json|1\/[\w-]|2\/[\w-]{2}|3\/[\w-]\/[\w-]{3}|[\w-]{2}\/[\w-]{2}\/[\w-]{4,})$/

/**
 * Serve the aged index of the sparse index `upstream` on a local port, and return the Bun
 * server. A file that the upstream lacks stays missing. A failed upstream read answers
 * 502, so cargo fails instead of resolving without the file.
 */
export function serveAgedIndex({ upstream, cutoffMs, allowed, request = registryFetch }) {
  return Bun.serve({
    hostname: '127.0.0.1',
    port: 0,
    async fetch(incoming) {
      const { pathname } = new URL(incoming.url)
      if (incoming.method !== 'GET' || !INDEX_PATH.test(pathname) || /[A-Z]/.test(pathname))
        return new Response('Not found', { status: 404 })
      let response
      try {
        response = await request(`${upstream}${pathname}`, {
          headers: { 'User-Agent': 'leapmux-dependency-maintenance' },
          signal: AbortSignal.timeout(30000),
        })
      }
      catch (error) {
        return new Response(`The crate index read failed: ${error.message}`, { status: 502 })
      }
      if (response.status === 404 || response.status === 410)
        return new Response('Not found', { status: response.status })
      if (!response.ok)
        return new Response(`The crate index answered ${response.status}.`, { status: 502 })
      const text = await response.text()
      if (pathname === '/config.json') {
        return new Response(text, {
          headers: { 'Content-Type': 'application/json' },
        })
      }
      try {
        // The filtered file differs from the upstream file, so no cache header passes on.
        return new Response(filterIndexFile(text, { cutoffMs, allowed }), {
          headers: { 'Content-Type': 'text/plain' },
        })
      }
      catch (error) {
        return new Response(`Invalid crate index file ${pathname}: ${error.message}`, { status: 502 })
      }
    },
  })
}

/**
 * Start the aged index in a child process, and return its sparse registry URL and the
 * function that stops it. The shim runs cargo through spawnSync, which blocks the event
 * loop of the shim, so a server in the shim's own process could not answer cargo. The
 * child stops when its standard input ends, so it never outlives the shim.
 */
export async function startAgedIndex({ upstream, cutoffMs, allowed }) {
  const child = spawn(process.execPath, [
    fileURLToPath(import.meta.url),
  ], {
    stdio: ['pipe', 'pipe', 'inherit'],
  })
  const exited = new Promise(resolve => child.once('close', resolve))
  const stop = async () => {
    child.stdin.end()
    await exited
  }
  child.stdin.write(`${JSON.stringify({
    upstream,
    cutoffMs,
    allowed: [...allowed],
  })}\n`)
  const port = await new Promise((resolve, reject) => {
    const lines = createInterface({ input: child.stdout })
    lines.once('line', (line) => {
      lines.close()
      resolve(Number(line))
    })
    child.once('error', reject)
    exited.then(code => reject(new Error(`The aged crate index stopped before it served cargo (exit ${code}).`)))
  })
  if (!Number.isSafeInteger(port) || port <= 0) {
    await stop()
    throw new Error(`The aged crate index reported an invalid port: ${port}`)
  }
  return { url: `sparse+http://127.0.0.1:${port}/`, close: stop }
}

if (import.meta.main) {
  // The first line of standard input configures the index. The end of the input, as when
  // the shim stops, stops the index.
  const lines = createInterface({ input: process.stdin })
  let server = null
  lines.on('line', (line) => {
    if (server)
      return
    const { upstream, cutoffMs, allowed } = JSON.parse(line)
    server = serveAgedIndex({
      upstream,
      cutoffMs,
      allowed: new Set(allowed),
    })
    process.stdout.write(`${server.port}\n`)
  })
  lines.once('close', () => {
    server?.stop(true)
    process.exit(0)
  })
}
