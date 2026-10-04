import type { NativeToolOutputFileIO, NativeToolOutputFileStat } from './nativeToolOutputFile'
import { Buffer } from 'node:buffer'
import { constants } from 'node:fs'
import { win32 } from 'node:path'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { readNativeToolOutputFile } from './nativeToolOutputFile'

function fixture(text = 'Complete\nnative77\n') {
  const bytes = Buffer.from(text)
  const stat: NativeToolOutputFileStat = { dev: 1, ino: 2, size: bytes.length, mtimeMs: 3, ctimeMs: 4, isFile: () => true, isSymbolicLink: () => false }
  const io: NativeToolOutputFileIO = {
    realpath: vi.fn(() => '/private/native'),
    lstat: vi.fn(() => stat),
    open: vi.fn(() => 0),
    stat: vi.fn(() => stat),
    read: vi.fn(() => bytes),
    close: vi.fn(),
  }
  return { io, stat, bytes }
}

describe('readNativeToolOutputFile', () => {
  it('reads exact stable bytes from descriptor zero without following the file link', () => {
    const f = fixture()
    expect(readNativeToolOutputFile('/private/native/result.txt', f.bytes.length, f.io)).toBe('Complete\nnative77\n')
    expect(f.io.open).toHaveBeenCalledWith('/private/native/result.txt', constants.O_RDONLY | constants.O_NOFOLLOW)
    expect(f.io.read).toHaveBeenCalledWith(0)
    expect(f.io.close).toHaveBeenCalledWith(0)
  })

  it('retains an empty native file at a zero-byte read limit', () => {
    const f = fixture('')
    expect(readNativeToolOutputFile('/private/native/result.txt', 0, f.io)).toBe('')
  })

  it('preserves a leading UTF-8 byte order mark in the complete native text', () => {
    const text = '\uFEFFNative output with a byte order mark\n'
    const f = fixture(text)
    expect(readNativeToolOutputFile('/private/native/result.txt', 100, f.io)).toBe(text)
  })

  it.each(['relative.txt', '/private/../native/result.txt', '/private/native/result\0.txt'])('rejects an invalid path %j before I/O', (path) => {
    const f = fixture()
    expect(() => readNativeToolOutputFile(path, 100, f.io)).toThrow('normalized file path')
    expect(f.io.realpath).not.toHaveBeenCalled()
  })

  it.each([-1, 0.5, Number.NaN, Number.POSITIVE_INFINITY, 16 * 1024 * 1024 + 1])('rejects an invalid limit %j before I/O', (limit) => {
    const f = fixture()
    expect(() => readNativeToolOutputFile('/private/native/result.txt', limit, f.io)).toThrow('read limit')
    expect(f.io.realpath).not.toHaveBeenCalled()
  })

  it.each([
    { size: -1 },
    { size: 101 },
    { size: Number.NaN },
    { isFile: () => false },
    { isSymbolicLink: () => true },
  ])('rejects an unsafe native file before open %j', (change) => {
    const f = fixture()
    f.io.lstat = () => ({ ...f.stat, ...change })
    expect(() => readNativeToolOutputFile('/private/native/result.txt', 100, f.io)).toThrow('regular file')
    expect(f.io.open).not.toHaveBeenCalled()
  })

  it('rejects replacement before open and closes the descriptor', () => {
    const f = fixture()
    f.io.stat = () => ({ ...f.stat, ino: 99 })
    expect(() => readNativeToolOutputFile('/private/native/result.txt', 100, f.io)).toThrow('before its descriptor opened')
    expect(f.io.read).not.toHaveBeenCalled()
    expect(f.io.close).toHaveBeenCalledWith(0)
  })

  it('rejects a partial read and closes the descriptor', () => {
    const f = fixture()
    f.io.read = () => f.bytes.subarray(1)
    expect(() => readNativeToolOutputFile('/private/native/result.txt', 100, f.io)).toThrow('declared file size')
    expect(f.io.close).toHaveBeenCalledWith(0)
  })

  it('rejects invalid UTF-8 without replacing its bytes', () => {
    const f = fixture('x')
    f.io.read = () => Uint8Array.from([0xFF])
    expect(() => readNativeToolOutputFile('/private/native/result.txt', 100, f.io)).toThrow()
    expect(f.io.close).toHaveBeenCalledWith(0)
  })

  it.each([{ ino: 3 }, { mtimeMs: 99 }, { ctimeMs: 99 }, { size: 0 }])('rejects mutation during the read %j', (change) => {
    const f = fixture()
    let reads = 0
    f.io.stat = () => ++reads === 1 ? f.stat : { ...f.stat, ...change }
    expect(() => readNativeToolOutputFile('/private/native/result.txt', 100, f.io)).toThrow('changed during the read')
    expect(f.io.close).toHaveBeenCalledWith(0)
  })

  it('rejects a changed parent directory after the read', () => {
    const f = fixture()
    let reads = 0
    f.io.realpath = () => ++reads === 1 ? '/private/native' : '/private/other'
    expect(() => readNativeToolOutputFile('/private/native/result.txt', 100, f.io)).toThrow('directory changed')
    expect(f.io.close).toHaveBeenCalledWith(0)
  })

  it('propagates an open failure without closing an absent descriptor', () => {
    const f = fixture()
    const failure = new Error('open failed')
    f.io.open = () => {
      throw failure
    }
    expect(() => readNativeToolOutputFile('/private/native/result.txt', 100, f.io)).toThrow(failure)
    expect(f.io.close).not.toHaveBeenCalled()
  })

  it('keeps both the read failure and the close failure', () => {
    const f = fixture()
    const readFailure = new Error('read failed')
    const closeFailure = new Error('close failed')
    f.io.read = () => {
      throw readFailure
    }
    f.io.close = () => {
      throw closeFailure
    }
    try {
      readNativeToolOutputFile('/private/native/result.txt', 100, f.io)
      throw new Error('The full tool output reader accepted two I/O failures.')
    }
    catch (error) {
      expect(error).toBeInstanceOf(AggregateError)
      if (!(error instanceof AggregateError))
        throw error
      expect(error.errors).toEqual([readFailure, closeFailure])
    }
  })

  it('propagates a close failure after a successful read', () => {
    const f = fixture()
    const failure = new Error('close failed')
    f.io.close = () => {
      throw failure
    }
    expect(() => readNativeToolOutputFile('/private/native/result.txt', 100, f.io)).toThrow(failure)
  })
})
vi.mock('node:path', async (importOriginal) => {
  const actual = await importOriginal<typeof import('node:path')>()
  return { ...actual, ...actual.posix }
})

afterEach(() => {
  vi.doUnmock('node:path')
  vi.resetModules()
})

async function windowsReader() {
  vi.resetModules()
  vi.doMock('node:path', () => ({ ...win32, default: win32 }))
  return (await import('./nativeToolOutputFile')).readNativeToolOutputFile
}

function windowsFile(): NativeToolOutputFileIO {
  const bytes = Buffer.from('complete native output')
  const stat = { dev: 1, ino: 2, size: bytes.length, mtimeMs: 3, ctimeMs: 4, isFile: () => true, isSymbolicLink: () => false }
  return {
    realpath: () => 'C:\\private\\native',
    lstat: () => stat,
    open: () => 0,
    stat: () => stat,
    read: () => bytes,
    close: vi.fn(),
  }
}

describe('readNativeToolOutputFile Windows paths', () => {
  it('reads the canonical native Windows path', async () => {
    const read = await windowsReader()
    expect(read('C:\\private\\native\\result.txt', 100, windowsFile())).toBe('complete native output')
  })

  it('reads the native slash form that provider libraries emit on Windows', async () => {
    const read = await windowsReader()
    expect(read('C:/private/native/result.txt', 100, windowsFile())).toBe('complete native output')
  })

  it.each(['C:/private/../native/result.txt', 'C:private/result.txt', 'native/result.txt'])('rejects a noncanonical or relative Windows path: %j', async (path) => {
    const read = await windowsReader()
    expect(() => read(path, 100, windowsFile())).toThrow('normalized file path')
  })
})
