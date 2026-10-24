import { chmodSync, mkdirSync, realpathSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import process from 'node:process'
import { LOOPBACK_HOSTNAMES, requireLoopbackHttpURL } from './agentEnvironmentInputs'
import { agentSearchPath, findBinary } from './binaryOnPath'
import { MAX_MOCK_REQUEST_BYTES } from './mockHttp'
import { quotePosixShellArgument } from './shellArguments'

export const LETTA_CHATGPT_USAGE_PATH = '/backend-api/wham/usage'

/** Redirect native ChatGPT HTTP calls without replacing the CLI's protocol or model loop. */
export function writeLettaTransport(options: { directory: string, shimsDirectory: string, origin: string }): void {
  const origin = requireLoopbackHttpURL(options.origin, 'The Letta mock', { originOnly: true }).origin
  const installed = findBinary('letta', { ...process.env, PATH: agentSearchPath(process.env.PATH) })
  if (installed === null)
    return
  mkdirSync(options.directory, { recursive: true })
  mkdirSync(options.shimsDirectory, { recursive: true })
  if (realpathSync(dirname(installed)) === realpathSync(options.shimsDirectory))
    throw new Error('The Letta launcher must resolve the installed CLI outside the private wrapper directory.')
  const preload = join(options.directory, 'letta-transport.cjs')
  writeFileSync(preload, lettaTransportSource(origin), { mode: 0o600 })
  const nodeOptions = `--require=${JSON.stringify(preload)}`
  const windows = process.platform === 'win32'
  const launcher = join(options.shimsDirectory, windows ? 'letta.cmd' : 'letta')
  const source = windows
    ? `@set "NODE_OPTIONS=${nodeOptions}"\r\n@call "${installed}" %*\r\n`
    : `#!/bin/sh\nexport NODE_OPTIONS=${quotePosixShellArgument(nodeOptions)}\nexec ${quotePosixShellArgument(installed)} "$@"\n`
  writeFileSync(launcher, source, { mode: 0o700 })
  chmodSync(launcher, 0o700)
}

/** The generated preload preserves Request objects, cancellation and native request bytes. */
export function lettaTransportSource(origin: string): string {
  requireLoopbackHttpURL(origin, 'The Letta mock', { originOnly: true })
  return `
const { zstdDecompressSync } = require('node:zlib');
const nativeFetch = globalThis.fetch;
const origin = ${JSON.stringify(origin)};
const loopback = new Set(${JSON.stringify([...LOOPBACK_HOSTNAMES])});
globalThis.fetch = async (input, init) => {
  const href = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
  const url = new URL(href);
  if (url.protocol === 'file:' || url.protocol === 'data:' || loopback.has(url.hostname))
    return nativeFetch(input, init);
  if (url.origin !== 'https://chatgpt.com' || (url.pathname !== ${JSON.stringify(LETTA_CHATGPT_USAGE_PATH)} && !url.pathname.startsWith('/backend-api/codex/')))
    throw new Error('The Letta test transport refused an unexpected remote origin: ' + url.origin);
  const request = new Request(input, init);
  request.signal.throwIfAborted();
  const redirected = new Request(new URL(url.pathname + url.search, origin), request);
  const headers = new Headers(redirected.headers);
  const options = { ...init, headers, signal: request.signal };
  if (headers.get('content-encoding') === 'zstd') {
    const compressed = Buffer.from(await redirected.arrayBuffer());
    options.body = zstdDecompressSync(compressed, { maxOutputLength: ${MAX_MOCK_REQUEST_BYTES} });
    headers.delete('content-encoding');
    headers.delete('content-length');
  }
  request.signal.throwIfAborted();
  return nativeFetch(redirected, options);
};
`
}
