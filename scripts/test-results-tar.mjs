import { Buffer } from 'node:buffer'
import { createReadStream, createWriteStream, statSync } from 'node:fs'
import { join } from 'node:path'
import { Readable } from 'node:stream'
import { pipeline } from 'node:stream/promises'
import { createGzip } from 'node:zlib'

const BLOCK_SIZE = 512

function header(name, type, size, mode = 0o644) {
  const block = Buffer.alloc(BLOCK_SIZE)
  block.write(name, 0, 100, 'ascii')
  for (const [offset, width, value] of [
    [100, 8, mode],
    [108, 8, 0],
    [116, 8, 0],
    [124, 12, size],
    [136, 12, 0],
  ])
    block.write(value.toString(8).padStart(width - 1, '0'), offset, width - 1, 'ascii')
  block.fill(32, 148, 156)
  block.write(type, 156, 1, 'ascii')
  block.write('ustar\0', 257, 6, 'ascii')
  block.write('00', 263, 2, 'ascii')
  const checksum = block.reduce((sum, byte) => sum + byte, 0)
  block.write(`${checksum.toString(8).padStart(6, '0')}\0 `, 148, 8, 'ascii')
  return block
}

function paxRecord(key, value) {
  const payload = Buffer.from(`${key}=${value}\n`, 'utf8')
  let length = payload.length + 2
  while (true) {
    const prefix = Buffer.from(`${length} `, 'ascii')
    const actual = prefix.length + payload.length
    if (actual === length)
      return Buffer.concat([prefix, payload])
    length = actual
  }
}

function timestamp(nanoseconds) {
  const sign = nanoseconds < 0n
    ? '-'
    : ''
  const magnitude = nanoseconds < 0n
    ? -nanoseconds
    : nanoseconds
  const fraction = (magnitude % 1000000000n).toString().padStart(9, '0').replace(/0+$/, '')
  return `${sign}${magnitude / 1000000000n}${fraction
    ? `.${fraction}`
    : ''}`
}

function padding(size) {
  return Buffer.alloc(Number((512n - BigInt(size) % 512n) % 512n))
}

/** Encode regular files with PAX path, size and time records; file data is consumed only after its headers. */
export async function* encodeTar(entries) {
  let index = 0
  for await (const entry of entries) {
    const path = entry.path
    const declaredSize = entry.size
    if (typeof path !== 'string' || path.includes('\0') || path.includes('\\') || /^[a-z]:/i.test(path) || path.split('/').some(component => !component || component === '.' || component === '..'))
      throw new Error('The archive entry path must be a safe relative path.')
    if (typeof declaredSize !== 'bigint' || declaredSize < 0n)
      throw new Error('The archive entry size must be a nonnegative bigint.')
    const pax = Buffer.concat([
      paxRecord('path', path),
      paxRecord('size', declaredSize),
      paxRecord('mtime', timestamp(entry.mtimeNs ?? 0n)),
    ])
    yield header(`PaxHeaders/${index}`, 'x', pax.length)
    yield pax
    yield padding(pax.length)
    // PAX supplies the full UTF-8 path and decimal size. The base header requires neither locale conversion nor a size limit.
    yield header(`File/${index++}`, '0', 0, entry.mode ?? 0o644)
    let size = 0n
    for await (const chunk of entry.readData()) {
      size += BigInt(chunk.byteLength)
      if (size > declaredSize)
        throw new Error(`The archive data exceeds the declared size: ${path}`)
      yield chunk
    }
    if (size !== declaredSize)
      throw new Error(`The archive data does not match the declared size: ${path}`)
    yield padding(size)
  }
  yield Buffer.alloc(2 * BLOCK_SIZE)
}

function* fileEntries(root, files) {
  for (const path of files) {
    const filename = join(root, path)
    const info = statSync(filename, { bigint: true })
    if (!info.isFile())
      throw new Error(`The archive source is not a regular file: ${path}`)
    yield {
      path,
      size: info.size,
      mode: Number(info.mode & 0o777n),
      mtimeNs: info.mtimeNs,
      readData: () => createReadStream(filename),
    }
  }
}

/** Stream a UTF-8 PAX archive through gzip without invoking the platform tar implementation. */
export async function writeTestResultsArchive(root, files, destination) {
  await pipeline(
    Readable.from(encodeTar(fileEntries(root, files)), { objectMode: false }),
    createGzip(),
    createWriteStream(destination, { flags: 'wx' }),
  )
}
