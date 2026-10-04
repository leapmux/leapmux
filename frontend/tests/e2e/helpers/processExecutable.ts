import { execFile } from 'node:child_process'
import { open, readlink, realpath } from 'node:fs/promises'
import { isAbsolute, win32 } from 'node:path'
import process from 'node:process'
import { promisify } from 'node:util'

const execute = promisify(execFile)
const COMMAND_TIMEOUT_MS = 60_000
const COMMAND_OUTPUT_LIMIT = 2 * 1024 * 1024

export interface ProcessExecutableIO {
  command: (file: string, args: string[]) => Promise<string>
  readLink: (path: string) => Promise<string>
  realPath: (path: string) => Promise<string>
  read: (path: string, offset: number, length: number) => Promise<Uint8Array>
  inspect?: (path: string, inspect: (read: (offset: number, length: number) => Promise<Uint8Array>) => Promise<boolean>) => Promise<boolean>
}

export interface ProcessBinaryReader {
  read: (offset: number, length: number) => Promise<Uint8Array>
  close: () => Promise<void>
}

/** Close one binary reader and preserve both inspection and close failures. */
export async function withProcessBinaryReader(reader: ProcessBinaryReader, inspect: (read: ProcessBinaryReader['read']) => Promise<boolean>): Promise<boolean> {
  let result: boolean | undefined
  let failure: unknown
  let failed = false
  try {
    result = await inspect(reader.read)
  }
  catch (error) {
    failure = error
    failed = true
  }
  try {
    await reader.close()
  }
  catch (error) {
    if (failed)
      throw new AggregateError([failure, error], 'The native binary inspection and reader close failed.')
    throw error
  }
  if (failed)
    throw failure
  if (result === undefined)
    throw new Error('The native binary inspection returned no result.')
  return result
}

async function openProcessBinary(path: string): Promise<ProcessBinaryReader> {
  const file = await open(path, 'r')
  return {
    read: async (offset, length) => {
      const buffer = new Uint8Array(length)
      const { bytesRead } = await file.read(buffer, 0, length, offset)
      return buffer.subarray(0, bytesRead)
    },
    close: () => file.close(),
  }
}

const nativeIO: ProcessExecutableIO = {
  command: async (file, args) => (await execute(file, args, { encoding: 'utf8', timeout: COMMAND_TIMEOUT_MS, maxBuffer: COMMAND_OUTPUT_LIMIT })).stdout,
  readLink: readlink,
  realPath: realpath,
  read: async (path, offset, length) => {
    const reader = await openProcessBinary(path)
    let data: Uint8Array | undefined
    await withProcessBinaryReader(reader, async (read) => {
      data = await read(offset, length)
      return true
    })
    if (data === undefined)
      throw new Error('The native binary reader returned no bytes.')
    return data
  },
  inspect: async (path, inspect) => withProcessBinaryReader(await openProcessBinary(path), inspect),
}

const DARWIN_PROCESS_PATH_QUERY = [
  'import ctypes,json,sys',
  'pid=int(sys.argv[1])',
  'lib=ctypes.CDLL("/usr/lib/libproc.dylib",use_errno=True)',
  'lib.proc_pidpath.argtypes=[ctypes.c_int,ctypes.c_void_p,ctypes.c_uint32]',
  'lib.proc_pidpath.restype=ctypes.c_int',
  'buffer=ctypes.create_string_buffer(4096)',
  'size=lib.proc_pidpath(pid,buffer,len(buffer))',
  'if size<=0: raise OSError(ctypes.get_errno(),"native process path query failed")',
  'print(json.dumps({"pid":pid,"path":buffer.value.decode("utf-8","strict")}))',
].join('\n')

/** Require one authoritative kernel path record for the exact requested PID. */
export function parseKernelProcessPath(text: string, pid: number): string {
  let value: unknown
  try {
    value = JSON.parse(text)
  }
  catch (cause) {
    throw new Error('The native executable query returned invalid kernel process identity.', { cause })
  }
  if (typeof value !== 'object' || value === null || Array.isArray(value) || !('pid' in value) || value.pid !== pid
    || !('path' in value) || typeof value.path !== 'string' || !isAbsolute(value.path) || value.path.includes('\0')) {
    throw new Error('The native executable query returned invalid kernel process identity.')
  }
  return value.path
}

/** Decode Mach-O executable identity without reading the complete binary. */
export async function isMachExecutable(path: string, read: ProcessExecutableIO['read']): Promise<boolean> {
  const header = await read(path, 0, 32)
  if (header.length < 4)
    throw new Error('The native process binary header is incomplete.')
  const view = new DataView(header.buffer, header.byteOffset, header.byteLength)
  const magic = view.getUint32(0, false)
  const thin = magic === 0xFEEDFACE || magic === 0xFEEDFACF || magic === 0xCEFAEDFE || magic === 0xCFFAEDFE
  if (thin) {
    if (header.length < 16)
      throw new Error('The native Mach-O header is incomplete.')
    return view.getUint32(12, magic === 0xCEFAEDFE || magic === 0xCFFAEDFE) === 2
  }
  const fat64 = magic === 0xCAFEBABF || magic === 0xBFBAFECA
  const fat = fat64 || magic === 0xCAFEBABE || magic === 0xBEBAFECA
  if (!fat)
    return false
  if (header.length < 8)
    throw new Error('The native fat Mach-O header is incomplete.')
  const little = magic === 0xBEBAFECA || magic === 0xBFBAFECA
  const count = view.getUint32(4, little)
  if (count < 1 || count > 64)
    throw new Error('The native fat Mach-O architecture count is invalid.')
  const stride = fat64 ? 32 : 20
  const table = await read(path, 8, count * stride)
  if (table.length !== count * stride)
    throw new Error('The native fat Mach-O architecture table is incomplete.')
  const architectures = new DataView(table.buffer, table.byteOffset, table.byteLength)
  let executable = false
  const ranges: Array<{ start: number, end: number }> = []
  for (let index = 0; index < count; index++) {
    const start = index * stride
    const offset = fat64 ? Number(architectures.getBigUint64(start + 8, little)) : architectures.getUint32(start + 8, little)
    const size = fat64 ? Number(architectures.getBigUint64(start + 16, little)) : architectures.getUint32(start + 12, little)
    const end = offset + size
    if (!Number.isSafeInteger(offset) || !Number.isSafeInteger(size) || !Number.isSafeInteger(end) || offset < 8 + table.length || size < 16)
      throw new Error('The native fat Mach-O architecture range is invalid.')
    if (ranges.some(range => offset < range.end && end > range.start))
      throw new Error('The native fat Mach-O architecture ranges overlap.')
    ranges.push({ start: offset, end })
    if ((await read(path, end - 1, 1)).length !== 1)
      throw new Error('The native fat Mach-O architecture extent exceeds the actual file.')
    const slice = await read(path, offset, 16)
    if (slice.length !== 16)
      throw new Error('The native fat Mach-O architecture header is incomplete.')
    const sliceView = new DataView(slice.buffer, slice.byteOffset, slice.byteLength)
    const sliceMagic = sliceView.getUint32(0, false)
    if (![0xFEEDFACE, 0xFEEDFACF, 0xCEFAEDFE, 0xCFFAEDFE].includes(sliceMagic))
      throw new Error('The native fat Mach-O architecture has no Mach-O header.')
    const sliceExecutable = sliceView.getUint32(12, sliceMagic === 0xCEFAEDFE || sliceMagic === 0xCFFAEDFE) === 2
    if (index > 0 && executable !== sliceExecutable)
      throw new Error('The native fat Mach-O architectures disagree about executable identity.')
    executable = sliceExecutable
  }
  return executable
}

/** Query one physical executable without treating the process's argv as executable evidence. */
export async function processExecutable(pid: number, platform: NodeJS.Platform = process.platform, io: ProcessExecutableIO = nativeIO): Promise<string> {
  if (!Number.isSafeInteger(pid) || pid <= 0)
    throw new Error('The physical executable query requires a positive safe PID.')
  let path: string
  if (platform === 'linux') {
    path = await io.readLink(`/proc/${pid}/exe`)
  }
  else if (platform === 'win32') {
    const text = await io.command('powershell.exe', ['-NoProfile', '-NonInteractive', '-Command', `Get-CimInstance Win32_Process -Filter 'ProcessId = ${pid}' | Select-Object ProcessId,ExecutablePath | ConvertTo-Json -Compress`])
    const decoded: unknown = JSON.parse(text)
    const records = Array.isArray(decoded) ? decoded : [decoded]
    const record = records[0]
    if (records.length !== 1 || typeof record !== 'object' || record === null || !('ProcessId' in record) || record.ProcessId !== pid
      || !('ExecutablePath' in record) || typeof record.ExecutablePath !== 'string' || !record.ExecutablePath.trim()) {
      throw new Error('The native executable query returned no unique exact Windows process.')
    }
    path = record.ExecutablePath
  }
  else if (platform === 'darwin') {
    // The kernel path avoids unrelated deleted text mappings and mutable argv titles.
    // macOS requires its system Python command for this libproc metadata query.
    const text = await io.command('/usr/bin/python3', ['-c', DARWIN_PROCESS_PATH_QUERY, String(pid)])
    path = parseKernelProcessPath(text, pid)
    const executable = io.inspect
      ? await io.inspect(path, read => isMachExecutable(path, (_path, offset, length) => read(offset, length)))
      : await isMachExecutable(path, io.read)
    if (!executable)
      throw new Error('The kernel process path is not a Mach-O executable.')
  }
  else {
    throw new Error(`The physical executable query does not support ${platform}.`)
  }
  if (!(platform === 'win32' ? win32.isAbsolute(path) : isAbsolute(path)))
    throw new Error('The native executable query returned a relative path.')
  return io.realPath(path)
}
