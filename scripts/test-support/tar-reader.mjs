import { spawnSync } from 'node:child_process'
import { resolve } from 'node:path'
import process from 'node:process'

const executable = resolve(import.meta.dirname, '../../.tmp', process.platform === 'win32'
  ? 'leapmux-test-tar-reader.exe'
  : 'leapmux-test-tar-reader')

function runReader(args, input) {
  const result = spawnSync(executable, args, { input, encoding: 'utf8' })
  if (result.error || result.status !== 0) {
    const reason = result.error?.message || result.stderr?.trim() || result.signal || `exit code ${result.status}`
    throw new Error(`The reference tar reader failed: ${reason}`)
  }
  return result.stdout
}

export function readTarHeaderForTests(input) {
  return JSON.parse(runReader(['header'], input))
}

/** Use Go's UTF-8 reader to check the extracted paths and bytes. */
export function extractTarForTests(archive, destination) {
  const count = Number(runReader(['extract', archive, destination]).trim())
  if (!Number.isSafeInteger(count) || count < 0)
    throw new Error('The reference tar reader returned an invalid file count.')
  return count
}
