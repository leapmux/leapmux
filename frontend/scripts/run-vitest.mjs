import { spawn } from 'node:child_process'
import { createRequire } from 'node:module'
import { dirname, join, resolve } from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'

const require = createRequire(import.meta.url)
const cli = join(dirname(require.resolve('vitest/package.json')), 'vitest.mjs')

/** Start the installed CLI with literal arguments on every platform. */
export function launchVitest(args) {
  const env = { ...process.env }
  // Node 25 introduced a web storage stub that conflicts with jsdom. Older Node versions reject this flag.
  if (Number.parseInt(process.versions.node, 10) >= 25) {
    const flag = '--no-experimental-webstorage'
    env.NODE_OPTIONS = env.NODE_OPTIONS ? `${env.NODE_OPTIONS} ${flag}` : flag
  }
  return spawn(process.execPath, [cli, ...args], { stdio: 'inherit', env })
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const child = launchVitest(process.argv.slice(2))
  child.once('error', (error) => {
    console.error(error)
    process.exitCode = 1
  })
  child.once('exit', (code, signal) => {
    if (signal)
      process.kill(process.pid, signal)
    else
      process.exitCode = code ?? 1
  })
}
