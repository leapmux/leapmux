import { Buffer } from 'node:buffer'
import { spawnSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { afterEach, describe, expect, it } from 'bun:test'
import { encodeTar, writeTestResultsArchive } from './test-results-tar.mjs'
import { scratchDirectories, writeFiles } from './test-support.mjs'

const scratch = scratchDirectories()
afterEach(scratch.cleanup)

async function bytes(iterator) {
  const chunks = []
  for await (const chunk of iterator)
    chunks.push(chunk)
  return Buffer.concat(chunks)
}

describe('encodeTar', () => {
  it('writes UTF-8 paths and file sizes that an independent Go reader decodes', async () => {
    const root = scratch.create('pax-header-reader')
    writeFiles(root, {
      'reader.go': `package main
import ("archive/tar"; "encoding/json"; "os"; "strconv")
func main() {
 h, err := tar.NewReader(os.Stdin).Next(); if err != nil { panic(err) }
 if err := json.NewEncoder(os.Stdout).Encode([]string{h.Name, strconv.FormatInt(h.Size,10), strconv.FormatInt(h.ModTime.UnixNano(),10)}); err != nil { panic(err) }
}
`,
    })
    for (const size of [0n, 8589934592n, 9007199254740993n]) {
      const path = `${'long-path/'.repeat(20)}日本語.log`
      const chunks = []
      let length = 0
      // PAX metadata occupies complete blocks. Stop after the following regular-file header, before any data is read.
      const iterator = encodeTar([
        {
          path,
          size,
          mtimeNs: -500000000n,
          readData: () => { throw new Error('The header reader must not request file data.') },
        },
      ])
      for await (const chunk of iterator) {
        chunks.push(chunk)
        length += chunk.length
        if (chunk.length === 512 && chunk[156] === 48 && length > 512)
          break
      }
      const result = spawnSync('go', [
        'run',
        join(root, 'reader.go'),
      ], {
        input: Buffer.concat(chunks),
        env: {
          ...process.env,
          GOWORK: 'off',
          GOTOOLCHAIN: 'local',
          GOFLAGS: '-mod=readonly',
        },
        encoding: 'utf8',
      })
      expect(result.status, result.stderr).toBe(0)
      expect(JSON.parse(result.stdout)).toEqual([
        path,
        String(size),
        '-500000000',
      ])
    }
  })

  it('handles empty input and rejects unsafe paths and invalid data sizes', async () => {
    expect(await bytes(encodeTar([]))).toEqual(Buffer.alloc(1024))
    for (const path of [
      '../escape',
      '/absolute',
      'C:/drive',
      'folder\\..\\escape',
      'nul\0path',
      '.',
      'folder/./report',
      'folder//report',
      'folder/',
    ]) {
      await expect(bytes(encodeTar([
        {
          path,
          size: 0n,
          readData: () => [],
        },
      ]))).rejects.toThrow('path')
    }
    await expect(bytes(encodeTar([
      {
        path: 'report',
        size: -1n,
        readData: () => [],
      },
    ]))).rejects.toThrow('size')
    await expect(bytes(encodeTar([
      {
        path: 'report',
        size: 1n,
        readData: () => [],
      },
    ]))).rejects.toThrow('size')
    await expect(bytes(encodeTar([
      {
        path: 'report',
        size: 0n,
        readData: () => [
          Buffer.from('extra'),
        ],
      },
    ]))).rejects.toThrow('size')
  })

  it('keeps the declared size stable while a data reader runs', async () => {
    const entry = {
      path: 'report',
      size: 0n,
      readData: () => {
        entry.size = 1n
        return [
          Buffer.from('x'),
        ]
      },
    }
    await expect(bytes(encodeTar([entry]))).rejects.toThrow('declared size')
    const valid = {
      path: 'report',
      size: 1n,
      readData: () => {
        valid.size = 0n
        return [
          Buffer.from('x'),
        ]
      },
    }
    const files = await new Bun.Archive(await bytes(encodeTar([valid]))).files()
    expect(await files.get('report').text()).toBe('x')
  })
})

describe('writeTestResultsArchive', () => {
  it('streams empty, binary and long Unicode entries across tar block limits', async () => {
    const root = scratch.create('streamed-tar')
    const files = {
      a: Buffer.alloc(0),
      aa: Buffer.from('one'),
      [`${'日本語/'.repeat(12)}report.log`]: Buffer.from([0, 255, 128, 42]),
    }
    for (const size of [511, 512, 513, 65537, 2 * 1024 * 1024])
      files[`data-${size}.bin`] = Buffer.alloc(size, 65)
    writeFiles(root, files)
    const archive = join(root, 'reports.tar.gz')
    await writeTestResultsArchive(root, Object.keys(files), archive)
    const destination = scratch.create('streamed-tar-extracted')
    const result = spawnSync('tar', ['-xzf', '-'], {
      cwd: destination,
      input: readFileSync(archive),
      encoding: 'utf8',
    })
    expect(result.status, result.stderr).toBe(0)
    for (const [path, contents] of Object.entries(files))
      expect(readFileSync(join(destination, path))).toEqual(contents)
  })

  it('rejects missing input and output write failures', async () => {
    const root = scratch.create('streamed-tar-failure')
    await expect(writeTestResultsArchive(root, ['missing'], join(root, 'missing.tar.gz'))).rejects.toBeInstanceOf(Error)
    writeFiles(root, { report: 'contents' })
    await expect(writeTestResultsArchive(root, ['report'], root)).rejects.toBeInstanceOf(Error)
  })
})
