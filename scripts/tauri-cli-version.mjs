// The version of the Tauri CLI that frontend/bun.lock resolves. The
// tauri-tools cache key hashes it, because the CLI decides which bundler
// tools Tauri downloads into its cache directory: an AppRun, a linuxdeploy
// AppImage, the linuxdeploy plugins, and WiX on Windows.
//
// Command line: `--hash` prints the sha256 of `@tauri-apps/cli@<version>`,
// the hash segment of the tauri-tools key, for the frontend/bun.lock of the
// working directory, as a workflow step runs it in the workspace.
import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'

// The package entry of bun.lock: `"@tauri-apps/cli": ["@tauri-apps/cli@<version>", …`.
const CLI_ENTRY = /^\s*"@tauri-apps\/cli": \["@tauri-apps\/cli@([^"]+)"/m

/** The Tauri CLI version that a bun.lock text resolves. */
export function tauriCliVersion(lockText) {
  const match = CLI_ENTRY.exec(lockText)
  if (!match)
    throw new Error('frontend/bun.lock resolves no @tauri-apps/cli package.')
  return match[1]
}

/** The tauri-tools key segment: the sha256 of the resolved Tauri CLI. */
export function tauriCliHash(lockText) {
  return createHash('sha256').update(`@tauri-apps/cli@${tauriCliVersion(lockText)}`).digest('hex')
}

if (import.meta.main) {
  try {
    if (process.argv.slice(2).join(' ') !== '--hash')
      throw new Error('Use --hash to print the sha256 of the resolved Tauri CLI.')
    process.stdout.write(`${tauriCliHash(readFileSync(join(process.cwd(), 'frontend', 'bun.lock'), 'utf8'))}\n`)
  }
  catch (error) {
    console.error(error.message)
    process.exitCode = 1
  }
}
