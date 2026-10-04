import { Buffer } from 'node:buffer'
import { closeSync, constants, fstatSync, lstatSync, openSync, readFileSync, realpathSync } from 'node:fs'
import { basename, dirname, isAbsolute, join, normalize, sep } from 'node:path'

export interface NativeToolOutputFileStat {
  dev: number
  ino: number
  size: number
  mtimeMs: number
  ctimeMs: number
  isFile: () => boolean
  isSymbolicLink: () => boolean
}

export interface NativeToolOutputFileIO {
  realpath: (path: string) => string
  lstat: (path: string) => NativeToolOutputFileStat
  open: (path: string, flags: number) => number
  stat: (fd: number) => NativeToolOutputFileStat
  read: (fd: number) => Uint8Array
  close: (fd: number) => void
}

const filesystem: NativeToolOutputFileIO = {
  realpath: realpathSync,
  lstat: lstatSync,
  open: openSync,
  stat: fstatSync,
  read: fd => readFileSync(fd),
  close: closeSync,
}

function sameFile(first: NativeToolOutputFileStat, second: NativeToolOutputFileStat): boolean {
  return first.dev === second.dev && first.ino === second.ino && first.size === second.size
    && first.mtimeMs === second.mtimeMs && first.ctimeMs === second.ctimeMs
}

/** Read one stable regular text file. The provider validates native reference ownership before this call. */
export function readNativeToolOutputFile(path: string, maxBytes = 4 * 1024 * 1024, io: NativeToolOutputFileIO = filesystem): string {
  path = sep === '\\' ? path.replaceAll('/', sep) : path
  if (!isAbsolute(path) || normalize(path) !== path || path.includes('\0') || basename(path) === '')
    throw new Error('The native full tool output requires an absolute normalized file path without NUL.')
  if (!Number.isSafeInteger(maxBytes) || maxBytes < 0 || maxBytes > 16 * 1024 * 1024)
    throw new Error('The native full tool output read limit requires zero through sixteen MiB.')
  const parent = io.realpath(dirname(path))
  const canonical = join(parent, basename(path))
  const before = io.lstat(canonical)
  if (before.isSymbolicLink() || !before.isFile() || !Number.isSafeInteger(before.size) || before.size < 0 || before.size > maxBytes)
    throw new Error('The native full tool output requires a complete regular file within the read limit.')
  const fd = io.open(canonical, constants.O_RDONLY | constants.O_NOFOLLOW)
  let text: string | undefined
  let failure: unknown
  let failed = false
  try {
    const opened = io.stat(fd)
    if (!opened.isFile() || !sameFile(before, opened))
      throw new Error('The native full tool output file changed before its descriptor opened.')
    const bytes = io.read(fd)
    if (Buffer.byteLength(bytes) !== before.size || bytes.byteLength > maxBytes)
      throw new Error('The native full tool output bytes differ from the complete declared file size.')
    text = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(bytes)
    const after = io.stat(fd)
    const current = io.lstat(canonical)
    if (!after.isFile() || !current.isFile() || current.isSymbolicLink() || !sameFile(before, after)
      || !sameFile(before, current) || io.realpath(dirname(path)) !== parent) {
      throw new Error('The native full tool output file or directory changed during the read.')
    }
  }
  catch (error) {
    failed = true
    failure = error
  }
  try {
    io.close(fd)
  }
  catch (closeError) {
    if (failed)
      throw new AggregateError([failure, closeError], 'The native full tool output read and descriptor close failed.')
    throw closeError
  }
  if (failed)
    throw failure
  if (text === undefined)
    throw new Error('The native full tool output read returned no complete text.')
  return text
}
