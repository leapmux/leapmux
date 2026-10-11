import type { ProcessExecutableIO } from './processExecutable'
import { realpath } from 'node:fs/promises'
import process from 'node:process'
import { describe, expect, it, vi } from 'vitest'
import { isMachExecutable, parseKernelProcessPath, processExecutable, withProcessBinaryReader } from './processExecutable'

function thin(type = 2, little = true): Uint8Array {
  const data = new Uint8Array(32)
  const view = new DataView(data.buffer)
  view.setUint32(0, 0xFEEDFACF, little)
  view.setUint32(12, type, little)
  return data
}

function fixture(files: Record<string, Uint8Array> = { '/native/amp': thin() }) {
  const command = vi.fn<ProcessExecutableIO['command']>().mockResolvedValue(JSON.stringify({ pid: 42, path: '/native/amp' }))
  const readLink = vi.fn<ProcessExecutableIO['readLink']>().mockResolvedValue('/native/amp')
  const realPath = vi.fn<ProcessExecutableIO['realPath']>().mockImplementation(async path => path)
  const read = vi.fn<ProcessExecutableIO['read']>().mockImplementation(async (path, offset, length) => {
    const file = files[path]
    if (!file)
      throw new Error('The actual executable file disappeared.')
    return file.subarray(offset, offset + length)
  })
  return { io: { command, readLink, realPath, read }, command, readLink, realPath, read }
}

describe('parseKernelProcessPath', () => {
  it('reads only the exact kernel PID and preserves paths with spaces', () => {
    expect(parseKernelProcessPath(JSON.stringify({ pid: 42, path: '/private/native amp' }), 42)).toBe('/private/native amp')
  })

  it.each(['', 'null', '[]', '{"pid":43,"path":"/native/amp"}', '{"pid":42,"path":"relative"}', '{"pid":42}', '{"pid":42,"path":4}', '[{"pid":42,"path":"/native/amp"},{"pid":42,"path":"/native/other"}]'])('refuses incomplete, foreign, or ambiguous kernel identity: %s', (text) => {
    expect(() => parseKernelProcessPath(text, 42)).toThrow('kernel process identity')
  })
})

describe('withProcessBinaryReader', () => {
  it('closes one reader after a successful inspection', async () => {
    const read = vi.fn(async () => thin())
    const close = vi.fn(async () => {})
    expect(await withProcessBinaryReader({ read, close }, async inspect => (await inspect(0, 32)).length === 32)).toBe(true)
    expect(read).toHaveBeenCalledExactlyOnceWith(0, 32)
    expect(close).toHaveBeenCalledTimes(1)
  })

  it('preserves inspection and close failures without closing twice', async () => {
    const inspection = new Error('The actual binary read failed.')
    const closure = new Error('The actual binary close failed.')
    const close = vi.fn(async () => {
      throw closure
    })
    let failure: unknown
    try {
      await withProcessBinaryReader({ read: async () => {
        throw inspection
      }, close }, async (read) => {
        await read(0, 32)
        return true
      })
    }
    catch (error) {
      failure = error
    }
    expect(failure).toBeInstanceOf(AggregateError)
    if (!(failure instanceof AggregateError))
      throw new Error('The binary reader did not preserve both failures.')
    expect(failure.errors).toEqual([inspection, closure])
    expect(close).toHaveBeenCalledTimes(1)
  })

  it('reports a close failure after a successful inspection', async () => {
    const closure = new Error('The actual binary close failed.')
    const close = vi.fn(async () => {
      throw closure
    })
    await expect(withProcessBinaryReader({ read: async () => thin(), close }, async () => true)).rejects.toBe(closure)
    expect(close).toHaveBeenCalledTimes(1)
  })
})

describe('isMachExecutable', () => {
  it.each([true, false])('reads a thin executable header with little endian %s', async (little) => {
    const { read } = fixture({ '/native/amp': thin(2, little) })
    expect(await isMachExecutable('/native/amp', read)).toBe(true)
    expect(read).toHaveBeenCalledExactlyOnceWith('/native/amp', 0, 32)
  })

  it('ignores a valid library mapping and rejects a truncated header', async () => {
    const { read } = fixture({ '/native/library': thin(6), '/native/short': new Uint8Array(3) })
    expect(await isMachExecutable('/native/library', read)).toBe(false)
    await expect(isMachExecutable('/native/short', read)).rejects.toThrow('incomplete')
  })

  it('reads fat architecture headers by offset without loading the complete file', async () => {
    const data = new Uint8Array(128)
    const view = new DataView(data.buffer)
    view.setUint32(0, 0xCAFEBABE)
    view.setUint32(4, 1)
    view.setUint32(16, 64)
    view.setUint32(20, 32)
    data.set(thin(), 64)
    const { read } = fixture({ '/native/amp': data })
    expect(await isMachExecutable('/native/amp', read)).toBe(true)
    expect(read.mock.calls).toEqual([['/native/amp', 0, 32], ['/native/amp', 8, 20], ['/native/amp', 95, 1], ['/native/amp', 64, 16]])
    view.setUint32(16, 2)
    await expect(isMachExecutable('/native/amp', read)).rejects.toThrow('range')
  })

  it.each([true, false])('reads a fat64 executable with little endian %s', async (little) => {
    const data = new Uint8Array(128)
    const view = new DataView(data.buffer)
    view.setUint32(0, 0xCAFEBABF, little)
    view.setUint32(4, 1, little)
    view.setBigUint64(16, 64n, little)
    view.setBigUint64(24, 32n, little)
    data.set(thin(2, little), 64)
    const { read } = fixture({ '/native/amp': data })
    expect(await isMachExecutable('/native/amp', read)).toBe(true)
    view.setBigUint64(16, BigInt(Number.MAX_SAFE_INTEGER) + 1n, little)
    await expect(isMachExecutable('/native/amp', read)).rejects.toThrow('range')
  })

  it('refuses an incomplete fat table and an absent architecture header', async () => {
    const data = new Uint8Array(32)
    const view = new DataView(data.buffer)
    view.setUint32(0, 0xCAFEBABE)
    view.setUint32(4, 2)
    const { read } = fixture({ '/native/amp': data })
    await expect(isMachExecutable('/native/amp', read)).rejects.toThrow('table is incomplete')
    view.setUint32(4, 1)
    view.setUint32(16, 64)
    view.setUint32(20, 32)
    await expect(isMachExecutable('/native/amp', read)).rejects.toThrow('extent')
  })

  it('refuses a declared architecture extent beyond the actual file', async () => {
    const data = new Uint8Array(128)
    const view = new DataView(data.buffer)
    view.setUint32(0, 0xCAFEBABE)
    view.setUint32(4, 1)
    view.setUint32(16, 64)
    view.setUint32(20, 100)
    data.set(thin(), 64)
    const { read } = fixture({ '/native/amp': data })
    await expect(isMachExecutable('/native/amp', read)).rejects.toThrow('extent')
  })

  it('refuses overlapping fat architecture ranges', async () => {
    const data = new Uint8Array(160)
    const view = new DataView(data.buffer)
    view.setUint32(0, 0xCAFEBABE)
    view.setUint32(4, 2)
    view.setUint32(16, 64)
    view.setUint32(20, 64)
    view.setUint32(36, 96)
    view.setUint32(40, 32)
    data.set(thin(), 64)
    data.set(thin(), 96)
    const { read } = fixture({ '/native/amp': data })
    await expect(isMachExecutable('/native/amp', read)).rejects.toThrow('overlap')
  })

  it('refuses an unsafe sum of individually safe fat offsets and sizes', async () => {
    const data = new Uint8Array(64)
    const view = new DataView(data.buffer)
    view.setUint32(0, 0xCAFEBABF)
    view.setUint32(4, 1)
    view.setBigUint64(16, BigInt(Number.MAX_SAFE_INTEGER - 8))
    view.setBigUint64(24, 32n)
    const read = vi.fn<ProcessExecutableIO['read']>().mockImplementation(async (_path, offset, length) => offset < 64 ? data.subarray(offset, offset + length) : thin().subarray(0, length))
    await expect(isMachExecutable('/native/amp', read)).rejects.toThrow('range')
  })
})

describe('processExecutable', () => {
  it('uses the exact Darwin kernel path without reading deleted unrelated mappings', async () => {
    const { io, command, read } = fixture({ '/native/amp': thin() })
    command.mockResolvedValue(JSON.stringify({ pid: 42, path: '/native/amp' }))
    expect(await processExecutable(42, 'darwin', io)).toBe('/native/amp')
    expect(command).toHaveBeenCalledExactlyOnceWith('/usr/bin/python3', expect.arrayContaining(['-c', '42']))
    expect(read.mock.calls.every(([path]) => path === '/native/amp')).toBe(true)
  })

  it.each([{ pid: 43, path: '/native/amp' }, { pid: 42, path: '' }, { pid: 42, path: 'relative' }, { path: '/native/amp' }])('refuses invalid Darwin kernel process identity: %j', async (record) => {
    const { io, command } = fixture()
    command.mockResolvedValue(JSON.stringify(record))
    await expect(processExecutable(42, 'darwin', io)).rejects.toThrow('kernel process identity')
  })

  it('refuses a non-executable kernel path and retains the actual native query failure', async () => {
    const { io, command } = fixture({ '/native/library': thin(6) })
    command.mockResolvedValue(JSON.stringify({ pid: 42, path: '/native/library' }))
    await expect(processExecutable(42, 'darwin', io)).rejects.toThrow('Mach-O executable')
    const failure = new Error('The exact native process disappeared.')
    command.mockRejectedValueOnce(failure)
    await expect(processExecutable(42, 'darwin', io)).rejects.toBe(failure)
  })

  it('queries only one native Darwin PID and reads only its kernel executable', async () => {
    const { io, command } = fixture({ '/native/amp': thin(), '/native/library': thin(6) })
    command.mockResolvedValue(JSON.stringify({ pid: 42, path: '/native/amp' }))
    expect(await processExecutable(42, 'darwin', io)).toBe('/native/amp')
    expect(command).toHaveBeenCalledExactlyOnceWith('/usr/bin/python3', expect.arrayContaining(['-c', '42']))
  })

  it('refuses ambiguous kernel records and preserves actual file-read errors', async () => {
    const { io, command } = fixture({ '/native/amp': thin(), '/native/other': thin() })
    command.mockResolvedValue(JSON.stringify([{ pid: 42, path: '/native/amp' }, { pid: 42, path: '/native/other' }]))
    await expect(processExecutable(42, 'darwin', io)).rejects.toThrow('kernel process identity')
    command.mockResolvedValue(JSON.stringify({ pid: 42, path: '/native/missing' }))
    await expect(processExecutable(42, 'darwin', io)).rejects.toThrow('disappeared')
  })

  it('reads the exact Linux executable link', async () => {
    const { io, readLink } = fixture()
    expect(await processExecutable(42, 'linux', io)).toBe('/native/amp')
    expect(readLink).toHaveBeenCalledExactlyOnceWith('/proc/42/exe')
    readLink.mockResolvedValue('relative')
    await expect(processExecutable(42, 'linux', io)).rejects.toThrow('relative path')
  })

  it('reads one exact Windows native executable record', async () => {
    const { io, command } = fixture()
    command.mockResolvedValue(JSON.stringify({ ProcessId: 42, ExecutablePath: 'C:\\native\\amp.exe' }))
    expect(await processExecutable(42, 'win32', io)).toBe('C:\\native\\amp.exe')
    command.mockResolvedValue(JSON.stringify([{ ProcessId: 42, ExecutablePath: 'C:\\native\\amp.exe' }, { ProcessId: 42, ExecutablePath: 'C:\\native\\other.exe' }]))
    await expect(processExecutable(42, 'win32', io)).rejects.toThrow('unique exact Windows')
    command.mockResolvedValue(JSON.stringify({ ProcessId: 43, ExecutablePath: 'C:\\native\\amp.exe' }))
    await expect(processExecutable(42, 'win32', io)).rejects.toThrow('unique exact Windows')
  })

  it('preserves a native command failure and refuses a missing Windows executable', async () => {
    const { io, command } = fixture()
    const failure = new Error('The native executable query failed.')
    command.mockRejectedValueOnce(failure)
    await expect(processExecutable(42, 'darwin', io)).rejects.toBe(failure)
    command.mockResolvedValue('{"ProcessId":42,"ExecutablePath":null}')
    await expect(processExecutable(42, 'win32', io)).rejects.toThrow('unique exact Windows')
  })

  it.each([0, -1, 0.5, Number.NaN, Number.POSITIVE_INFINITY, Number.MAX_SAFE_INTEGER + 1])('refuses an invalid PID before I/O: %s', async (pid) => {
    const { io, command, readLink } = fixture()
    await expect(processExecutable(pid, 'darwin', io)).rejects.toThrow('positive safe PID')
    expect(command).not.toHaveBeenCalled()
    expect(readLink).not.toHaveBeenCalled()
  })

  it('reads this actual test process through the native platform transport', async () => {
    expect(await processExecutable(process.pid)).toBe(await realpath(process.execPath))
  })
})
