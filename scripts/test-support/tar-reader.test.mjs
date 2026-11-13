import { Buffer } from 'node:buffer'
import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { afterEach, describe, expect, it } from 'bun:test'
import { writeTestResultsArchive } from '../test-results-tar.mjs'
import { scratchDirectories, writeFiles } from '../test-support.mjs'
import { extractTarForTests } from './tar-reader.mjs'

const scratch = scratchDirectories()
afterEach(scratch.cleanup)

describe('extractTarForTests', () => {
  it('extracts complete Unicode and binary data with an independent reader', async () => {
    const root = scratch.create('reference-reader')
    writeFiles(root, {
      '日本語.bin': Buffer.from([0, 255, 128, 42]),
    })
    const archive = join(root, 'reports.tar.gz')
    await writeTestResultsArchive(root, ['日本語.bin'], archive)
    const destination = scratch.create('reference-extracted')
    expect(extractTarForTests(archive, destination)).toBe(1)
    expect(readFileSync(join(destination, '日本語.bin'))).toEqual(Buffer.from([0, 255, 128, 42]))
  })

  it('rejects missing archives and corrupt gzip checksums', async () => {
    const root = scratch.create('reference-reader-errors')
    const archive = join(root, 'reports.tar.gz')
    const destination = scratch.create('reference-errors-extracted')
    expect(() => extractTarForTests(archive, destination)).toThrow('reference tar reader failed')
    writeFiles(root, { report: 'contents' })
    await writeTestResultsArchive(root, ['report'], archive)
    const bytes = readFileSync(archive)
    bytes[bytes.length - 8] ^= 128
    writeFileSync(archive, bytes)
    expect(() => extractTarForTests(archive, destination)).toThrow('checksum')
  })
})
