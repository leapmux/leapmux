import type { ChildProcess } from 'node:child_process'
import type { NativeStartupLaunch } from './nativeStartupWrapper'
import { Buffer } from 'node:buffer'
import { spawn } from 'node:child_process'
import { once } from 'node:events'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { connect, Server, Socket } from 'node:net'
import { dirname, join, resolve } from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createNativeStartupWrapper, resolveNativeStartupLaunch } from './nativeStartupWrapper'
import { stopProcess } from './process'

const scratchRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../.tmp')
let directory: string
const children: ChildProcess[] = []
const wrappers: Awaited<ReturnType<typeof createNativeStartupWrapper>>[] = []

beforeEach(() => {
  mkdirSync(scratchRoot, { recursive: true })
  directory = mkdtempSync(join(scratchRoot, 'startup-wrapper-unit-'))
})

afterEach(async () => {
  const results = await Promise.allSettled([
    ...wrappers.splice(0).map(wrapper => wrapper.dispose()),
    ...children.splice(0).map(child => stopProcess(child)),
  ])
  const failures = results.filter(result => result.status === 'rejected')
  if (failures.length)
    throw new AggregateError(failures.map(result => result.reason), 'Startup wrapper test cleanup failed.')
  rmSync(directory, { recursive: true, force: true })
})

interface StartupObservationFixtureOptions {
  failRuntime?: boolean
  observeEnvironment?: readonly string[]
}

async function fixture(options: StartupObservationFixtureOptions = {}, fixtureDirectory = directory, launch: Pick<NativeStartupLaunch, 'passThroughWhen'> = {}) {
  mkdirSync(fixtureDirectory, { recursive: true })
  const outputFile = join(fixtureDirectory, 'actual-native-run.json')
  const program = join(fixtureDirectory, 'real-native-fixture.cjs')
  writeFileSync(program, `require('node:fs').writeFileSync(${JSON.stringify(outputFile)},JSON.stringify({argv:process.argv.slice(2),marker:process.env.NATIVE_WRAPPER_UNIT_MARKER}));process.exitCode=Number(process.env.NATIVE_WRAPPER_UNIT_EXIT??0)`)
  const wrapper = await createNativeStartupWrapper(join(fixtureDirectory, 'bin'), {
    binaryName: 'native-fixture',
    executable: process.execPath,
    args: [program],
    holdWhen: ['runtime'],
    ...launch,
  }, options)
  wrappers.push(wrapper)
  const start = (args: string[], input?: string, runtime: { environment?: NodeJS.ProcessEnv, workingDir?: string } = {}) => {
    const child = spawn(process.execPath, [wrapper.scriptPath, ...args], {
      env: { ...process.env, NATIVE_WRAPPER_UNIT_MARKER: 'isolated-environment', ...runtime.environment },
      ...(runtime.workingDir === undefined ? {} : { cwd: runtime.workingDir }),
      stdio: ['pipe', 'pipe', 'pipe'],
    })
    children.push(child)
    if (input !== undefined)
      child.stdin?.end(input)
    return child
  }
  return { wrapper, outputFile, program, start }
}

function observationProgram(program: string, outputFile: string, keys: readonly string[]): void {
  writeFileSync(program, `const keys=${JSON.stringify(keys)};require('node:fs').writeFileSync(${JSON.stringify(outputFile)},JSON.stringify({workingDir:process.cwd(),environment:Object.fromEntries(keys.map(key=>[key,process.env[key]??null]))}))`)
}

async function handshakeVerdict(wrapper: Awaited<ReturnType<typeof createNativeStartupWrapper>>, value: unknown, expectedRefusal?: string): Promise<'accepted' | 'refused'> {
  const socket = connect(wrapper.endpoint.port, '127.0.0.1')
  const closed = once(socket, 'close')
  let reply = ''
  socket.setEncoding('utf8')
  socket.on('data', (data: string) => reply += data)
  try {
    await once(socket, 'connect')
    socket.end(`${JSON.stringify(value)}\n`)
    const verdict = await Promise.race([
      wrapper.entry.then(() => 'accepted' as const),
      closed.then(() => 'refused' as const),
    ])
    if (verdict === 'refused') {
      if (expectedRefusal === undefined)
        expect(reply).toBe('')
      else
        expect(JSON.parse(reply)).toEqual({ error: expectedRefusal })
    }
    return verdict
  }
  finally {
    socket.destroy()
    await closed
  }
}

function firstHandshakeFragment() {
  const emit = Server.prototype.emit
  let receivedFirst!: () => void
  const received = new Promise<void>(resolve => receivedFirst = resolve)
  const connection = vi.spyOn(Server.prototype, 'emit').mockImplementation(function (this: Server, event, ...args) {
    if (event === 'connection') {
      const socket = args[0]
      if (socket instanceof Socket)
        socket.once('data', receivedFirst)
    }
    return emit.call(this, event, ...args)
  })
  return { received, restore: () => connection.mockRestore() }
}

function reportServerCloseFailure(failure: Error) {
  const close = Server.prototype.close
  return vi.spyOn(Server.prototype, 'close').mockImplementation(function (this: Server, callback?: (error?: Error) => void) {
    return close.call(this, error => callback?.(error ?? failure))
  })
}

describe('resolveNativeStartupLaunch', () => {
  // `hubSpawnEnv` merges the shared agent environment that global setup records. A unit run records none.
  beforeEach(() => {
    vi.stubEnv('E2E_STATE_PATH', '')
  })
  afterEach(() => {
    vi.unstubAllEnvs()
  })

  /** Write a file that the lookup can find. The lookup must never run it. */
  function nativeExecutable(parent: string, name: string): string {
    mkdirSync(parent, { recursive: true })
    const executable = join(parent, name)
    writeFileSync(executable, 'This fixture must never run.\n', { mode: 0o755 })
    return executable
  }

  it('finds the isolated executable and retains native arguments without running it', () => {
    const executable = join(directory, 'private-native')
    writeFileSync(executable, 'This fixture must never run.\n', { mode: 0o755 })
    const launch = { binaryName: 'private-native', args: ['native-prefix'], holdWhen: ['runtime', '--rpc'], passThroughWhen: ['--probe'], lazy: true }
    expect(resolveNativeStartupLaunch({ PATH: directory }, launch)).toEqual({ ...launch, executable })
    expect(launch).toEqual({ binaryName: 'private-native', args: ['native-prefix'], holdWhen: ['runtime', '--rpc'], passThroughWhen: ['--probe'], lazy: true })
  })

  // The Worker starts with `hubSpawnEnv(agentEnv)`, and an agent environment with no PATH inherits the process PATH.
  it('finds the executable through the process PATH when the agent environment holds no PATH', () => {
    const executable = nativeExecutable(join(directory, 'inherited'), 'inherited-native')
    vi.stubEnv('PATH', join(directory, 'inherited'))
    expect(resolveNativeStartupLaunch({ HOME: directory }, { binaryName: 'inherited-native' }).executable).toBe(executable)
  })

  it('prefers the PATH of the agent environment to the process PATH, as the Worker does', () => {
    nativeExecutable(join(directory, 'process'), 'shared-native')
    const agentExecutable = nativeExecutable(join(directory, 'agent'), 'shared-native')
    vi.stubEnv('PATH', join(directory, 'process'))
    expect(resolveNativeStartupLaunch({ PATH: join(directory, 'agent') }, { binaryName: 'shared-native' }).executable).toBe(agentExecutable)
  })

  it('rejects an absent agent environment', () => {
    expect(() => resolveNativeStartupLaunch(undefined, { binaryName: 'native-fixture' })).toThrow('private agent environment')
  })

  it('rejects an executable that neither PATH holds, and states its name', () => {
    vi.stubEnv('PATH', directory)
    const reason = 'The isolated absent-native executable is absent from the PATH that the Worker receives'
    expect(() => resolveNativeStartupLaunch({ PATH: directory }, { binaryName: 'absent-native' })).toThrow(reason)
    expect(() => resolveNativeStartupLaunch({}, { binaryName: 'absent-native' })).toThrow(reason)
  })

  it.each(['', '.', '..', 'bin/native', 'bin\\native', 'native\0'])('rejects the executable name %j, which is not one file-name component', (binaryName) => {
    expect(() => resolveNativeStartupLaunch({ PATH: directory }, { binaryName })).toThrow('one file-name component')
  })
})

describe('createNativeStartupWrapper', () => {
  it('retains both script-write and listener cleanup failures', async () => {
    const wrapperDirectory = join(directory, 'write-refusal')
    mkdirSync(join(wrapperDirectory, 'native-fixture.startup.cjs'), { recursive: true })
    const cleanupFailure = new Error('The listener cleanup reported a failure.')
    const close = reportServerCloseFailure(cleanupFailure)
    try {
      await expect(createNativeStartupWrapper(wrapperDirectory, { binaryName: 'native-fixture', executable: process.execPath })).rejects.toMatchObject({ errors: [expect.objectContaining({ code: 'EISDIR' }), cleanupFailure] })
    }
    finally {
      close.mockRestore()
    }
  })

  it('retains both invalid-address and listener cleanup failures', async () => {
    const cleanupFailure = new Error('The listener cleanup reported a failure.')
    const close = reportServerCloseFailure(cleanupFailure)
    const address = vi.spyOn(Server.prototype, 'address').mockReturnValue(null)
    try {
      await expect(createNativeStartupWrapper(join(directory, 'address-refusal'), { binaryName: 'native-fixture', executable: process.execPath })).rejects.toMatchObject({ errors: [expect.objectContaining({ message: 'The startup wrapper received no TCP listener address.' }), cleanupFailure] })
    }
    finally {
      address.mockRestore()
      close.mockRestore()
    }
  })

  it('observes the actual selected environment and cwd before forwarding them unchanged', async () => {
    const selected = ['TMPDIR', 'TMP', 'TEMP']
    const { wrapper, program, outputFile, start } = await fixture({ failRuntime: false, observeEnvironment: selected })
    const workingDir = join(directory, 'actual native cwd 漢字')
    const temporary = join(directory, 'actual private temporary')
    mkdirSync(workingDir)
    mkdirSync(temporary)
    observationProgram(program, outputFile, selected)
    const environment = { TMPDIR: temporary, TMP: temporary, TEMP: temporary }
    const child = start(['runtime'], undefined, { workingDir, environment: { ...environment, PRIVATE_CREDENTIAL_NOT_SELECTED: 'controlled-credential-marker' } })
    const exited = once(child, 'exit')
    const entry = await wrapper.entry
    expect(entry).toEqual({ pid: child.pid, argv: ['runtime'], observation: { workingDir, environment } })
    expect(JSON.stringify(entry)).not.toContain('controlled-credential-marker')
    expect(JSON.stringify(entry)).not.toContain('PRIVATE_CREDENTIAL_NOT_SELECTED')
    expect(existsSync(outputFile)).toBe(false)
    await wrapper.release()
    expect(await exited).toEqual([0, null])
    expect(JSON.parse(readFileSync(outputFile, 'utf8'))).toEqual({ workingDir, environment })
  })

  it('observes cwd with an explicit empty key selection', async () => {
    const { wrapper, start } = await fixture({ failRuntime: false, observeEnvironment: [] })
    const child = start(['runtime'])
    const exited = once(child, 'exit')
    expect(await wrapper.entry).toEqual({ pid: child.pid, argv: ['runtime'], observation: { workingDir: process.cwd(), environment: {} } })
    await wrapper.release()
    expect(await exited).toEqual([0, null])
  })

  it('preserves missing, empty, and Unicode selected values', async () => {
    const keys = ['PRIVATE_ABSENT_VALUE', 'PRIVATE_EMPTY_VALUE', 'PRIVATE_UNICODE_VALUE']
    const { wrapper, program, outputFile, start } = await fixture({ failRuntime: false, observeEnvironment: keys })
    observationProgram(program, outputFile, keys)
    const child = start(['runtime'], undefined, { environment: { PRIVATE_ABSENT_VALUE: undefined, PRIVATE_EMPTY_VALUE: '', PRIVATE_UNICODE_VALUE: '한글π🙂\n' } })
    const exited = once(child, 'exit')
    const expected = { workingDir: process.cwd(), environment: { PRIVATE_ABSENT_VALUE: null, PRIVATE_EMPTY_VALUE: '', PRIVATE_UNICODE_VALUE: '한글π🙂\n' } }
    expect(await wrapper.entry).toEqual({ pid: child.pid, argv: ['runtime'], observation: expected })
    await wrapper.release()
    expect(await exited).toEqual([0, null])
    expect(JSON.parse(readFileSync(outputFile, 'utf8'))).toEqual(expected)
  })

  it('copies the selected keys before the caller mutates its array', async () => {
    const keys = ['PRIVATE_FIRST_VALUE']
    const { wrapper, start } = await fixture({ failRuntime: false, observeEnvironment: keys })
    keys.push('PRIVATE_LATER_VALUE')
    const child = start(['runtime'], undefined, { environment: { PRIVATE_FIRST_VALUE: 'first', PRIVATE_LATER_VALUE: 'later' } })
    const exited = once(child, 'exit')
    expect(await wrapper.entry).toEqual({ pid: child.pid, argv: ['runtime'], observation: { workingDir: process.cwd(), environment: { PRIVATE_FIRST_VALUE: 'first' } } })
    await wrapper.release()
    expect(await exited).toEqual([0, null])
  })

  it('uses the platform case rule for duplicate environment keys', async () => {
    const options = { failRuntime: false, observeEnvironment: ['TMPDIR', 'tmpdir'] }
    if (process.platform === 'win32') {
      await expect(fixture(options)).rejects.toThrow(/environment|observation/i)
      return
    }
    const { wrapper, start } = await fixture(options)
    const environment = { TMPDIR: 'upper', tmpdir: 'lower' }
    const child = start(['runtime'], undefined, { environment })
    const exited = once(child, 'exit')
    expect(await wrapper.entry).toEqual({ pid: child.pid, argv: ['runtime'], observation: { workingDir: process.cwd(), environment } })
    await wrapper.release()
    expect(await exited).toEqual([0, null])
  })

  it.each([
    { label: 'null selection', observeEnvironment: null },
    { label: 'string selection', observeEnvironment: 'TMPDIR' },
    { label: 'numeric selection', observeEnvironment: 0 },
    { label: 'empty key', observeEnvironment: [''] },
    { label: 'assignment key', observeEnvironment: ['TMPDIR=value'] },
    { label: 'NUL key', observeEnvironment: ['TMP\0DIR'] },
    { label: 'space key', observeEnvironment: ['TMP DIR'] },
    { label: 'newline key', observeEnvironment: ['TMP\nDIR'] },
    { label: 'numeric key', observeEnvironment: [0] },
    { label: 'duplicate key', observeEnvironment: ['TMPDIR', 'TMPDIR'] },
  ])('rejects an invalid environment observation selection: $label', async (invalid) => {
    await expect(Reflect.apply(fixture, undefined, [{ failRuntime: false, observeEnvironment: invalid.observeEnvironment }])).rejects.toThrow(/environment|observation/i)
  })

  it.each([
    { label: 'absent observation', observation: undefined },
    { label: 'null observation', observation: null },
    { label: 'array observation', observation: [] },
    { label: 'absent cwd', observation: { environment: { TMPDIR: 'actual' } } },
    { label: 'empty cwd', observation: { workingDir: '', environment: { TMPDIR: 'actual' } } },
    { label: 'relative cwd', observation: { workingDir: 'relative/path', environment: { TMPDIR: 'actual' } } },
    { label: 'NUL cwd', observation: { workingDir: `${process.cwd()}\0invalid`, environment: { TMPDIR: 'actual' } } },
    { label: 'absent environment', observation: { workingDir: process.cwd() } },
    { label: 'null environment', observation: { workingDir: process.cwd(), environment: null } },
    { label: 'array environment', observation: { workingDir: process.cwd(), environment: [] } },
    { label: 'missing selected key', observation: { workingDir: process.cwd(), environment: {} } },
    { label: 'unselected key', observation: { workingDir: process.cwd(), environment: { TMPDIR: 'actual', PRIVATE_UNSELECTED_VALUE: 'unselected' } } },
    { label: 'numeric value', observation: { workingDir: process.cwd(), environment: { TMPDIR: 0 } } },
    { label: 'object value', observation: { workingDir: process.cwd(), environment: { TMPDIR: {} } } },
  ])('refuses a malformed requested observation before accepting the process: $label', async ({ observation }) => {
    const { wrapper } = await fixture({ failRuntime: false, observeEnvironment: ['TMPDIR'] })
    expect(await handshakeVerdict(wrapper, { nonce: wrapper.endpoint.nonce, pid: process.pid, argv: ['runtime'], observation }, 'The startup environment observation was refused.')).toBe('refused')
  })

  it('accepts the exact handshake byte limit with observed Unicode values', async () => {
    const { wrapper } = await fixture({ failRuntime: false, observeEnvironment: ['PRIVATE_UNICODE_VALUE'] })
    const socket = connect(wrapper.endpoint.port, '127.0.0.1')
    const closed = once(socket, 'close')
    try {
      await once(socket, 'connect')
      const observation = { workingDir: process.cwd(), environment: { PRIVATE_UNICODE_VALUE: '한글🙂' } }
      const frame = JSON.stringify({ nonce: wrapper.endpoint.nonce, pid: process.pid, argv: ['runtime'], observation })
      const padding = ' '.repeat(65_536 - Buffer.byteLength(frame) - 1)
      socket.write(`${frame}${padding}\n`)
      expect(await wrapper.entry).toEqual({ pid: process.pid, argv: ['runtime'], observation })
    }
    finally {
      socket.destroy()
      await closed
    }
  })

  it('preserves an observed value when the handshake splits its UTF-8 bytes', async () => {
    const { wrapper } = await fixture({ failRuntime: false, observeEnvironment: ['PRIVATE_UNICODE_VALUE'] })
    const first = firstHandshakeFragment()
    const socket = connect(wrapper.endpoint.port, '127.0.0.1')
    const closed = once(socket, 'close')
    try {
      await once(socket, 'connect')
      const observation = { workingDir: process.cwd(), environment: { PRIVATE_UNICODE_VALUE: '한글🙂' } }
      const frame = Buffer.from(`${JSON.stringify({ nonce: wrapper.endpoint.nonce, pid: process.pid, argv: ['runtime'], observation })}\n`)
      const split = frame.indexOf(Buffer.from('한')) + 1
      socket.write(frame.subarray(0, split))
      await first.received
      socket.write(frame.subarray(split))
      expect(await wrapper.entry).toEqual({ pid: process.pid, argv: ['runtime'], observation })
    }
    finally {
      first.restore()
      socket.destroy()
      await closed
    }
  })

  it('refuses oversized observation bytes before accepting the process', async () => {
    const { wrapper } = await fixture({ failRuntime: false, observeEnvironment: ['PRIVATE_UNICODE_VALUE'] })
    const value = { nonce: wrapper.endpoint.nonce, pid: process.pid, argv: ['runtime'], observation: { workingDir: process.cwd(), environment: { PRIVATE_UNICODE_VALUE: '한'.repeat(22_000) } } }
    expect(Buffer.byteLength(JSON.stringify(value))).toBeGreaterThan(65_536)
    expect(await handshakeVerdict(wrapper, value)).toBe('refused')
  })

  it('keeps a second observed wrapper live after the first wrapper closes', async () => {
    const first = await fixture({ failRuntime: false, observeEnvironment: ['TMPDIR'] }, join(directory, 'first-wrapper'))
    const second = await fixture({ failRuntime: false, observeEnvironment: ['TMPDIR'] }, join(directory, 'second-wrapper'))
    const firstChild = first.start(['runtime'], undefined, { environment: { TMPDIR: 'first' } })
    const secondChild = second.start(['runtime'], undefined, { environment: { TMPDIR: 'second' } })
    const firstExited = once(firstChild, 'exit')
    const secondExited = once(secondChild, 'exit')
    await Promise.all([first.wrapper.entry, second.wrapper.entry])
    await first.wrapper.dispose()
    expect(await firstExited).toEqual([125, null])
    expect(secondChild.exitCode).toBeNull()
    await second.wrapper.release()
    expect(await secondExited).toEqual([0, null])
  })

  it('preserves a Unicode argument when the handshake splits its UTF-8 bytes', async () => {
    const { wrapper } = await fixture()
    const first = firstHandshakeFragment()
    const socket = connect(wrapper.endpoint.port, '127.0.0.1')
    const closed = once(socket, 'close')
    try {
      await once(socket, 'connect')
      const argv = ['runtime', '한글😀']
      const handshake = Buffer.from(`${JSON.stringify({ nonce: wrapper.endpoint.nonce, pid: process.pid, argv })}\n`)
      const split = handshake.indexOf(Buffer.from('한')) + 1
      socket.write(handshake.subarray(0, split))
      await first.received
      socket.write(handshake.subarray(split))
      expect(await wrapper.entry).toEqual({ pid: process.pid, argv })
    }
    finally {
      first.restore()
      socket.destroy()
      await closed
    }
  })

  it('accepts the exact handshake byte limit', async () => {
    const { wrapper } = await fixture()
    const socket = connect(wrapper.endpoint.port, '127.0.0.1')
    const closed = once(socket, 'close')
    try {
      await once(socket, 'connect')
      const argv = ['runtime', '한글😀']
      const frame = JSON.stringify({ nonce: wrapper.endpoint.nonce, pid: process.pid, argv })
      const padding = ' '.repeat(65_536 - Buffer.byteLength(frame) - 1)
      socket.write(`${frame}${padding}\n`)
      expect(await wrapper.entry).toEqual({ pid: process.pid, argv })
    }
    finally {
      socket.destroy()
      await closed
    }
  })

  it('rejects a Unicode handshake above the byte limit before accepting its identity', async () => {
    const { wrapper } = await fixture()
    const socket = connect(wrapper.endpoint.port, '127.0.0.1')
    const closed = once(socket, 'close')
    try {
      await once(socket, 'connect')
      const frame = JSON.stringify({ nonce: wrapper.endpoint.nonce, pid: process.pid, argv: ['runtime', '한'.repeat(22_000)] })
      expect(frame.length).toBeLessThan(65_536)
      expect(Buffer.byteLength(frame)).toBeGreaterThan(65_536)
      socket.end(`${frame}\n`)
      const verdict = await Promise.race([
        wrapper.entry.then(() => 'accepted'),
        closed.then(() => 'refused'),
      ])
      expect(verdict).toBe('refused')
    }
    finally {
      socket.destroy()
    }
  })

  it('holds the native process and forwards its exact arguments and environment after release', async () => {
    const { wrapper, outputFile, start } = await fixture()
    const args = ['runtime', 'a space', 'a"quote', 'a\'quote', '$literal', '']
    const child = start(args)
    const exited = once(child, 'exit')
    expect(await wrapper.entry).toEqual({ pid: child.pid, argv: args })
    expect(existsSync(outputFile)).toBe(false)
    await wrapper.release()
    expect(await exited).toEqual([0, null])
    expect(JSON.parse(readFileSync(outputFile, 'utf8'))).toEqual({ argv: args, marker: 'isolated-environment' })
  })

  it('leaves queued stdin for the real native executable', async () => {
    const { wrapper, program, outputFile, start } = await fixture()
    writeFileSync(program, `let input='';process.stdin.setEncoding('utf8');process.stdin.on('data',chunk=>input+=chunk);process.stdin.on('end',()=>require('node:fs').writeFileSync(${JSON.stringify(outputFile)},input))`)
    const child = start(['runtime'], 'queued native input\n')
    const exited = once(child, 'exit')
    await wrapper.entry
    await wrapper.release()
    expect(await exited).toEqual([0, null])
    expect(readFileSync(outputFile, 'utf8')).toBe('queued native input\n')
  })

  it.each([{ args: ['--version'] }, { args: ['runtime', '--help'] }, { args: ['catalog'] }])('executes a discovery call without a runtime hold: %j', async ({ args }) => {
    const { outputFile, start } = await fixture()
    const child = start(args)
    expect(await once(child, 'exit')).toEqual([0, null])
    expect(JSON.parse(readFileSync(outputFile, 'utf8')).argv).toEqual(args)
  })

  it('preserves the real native exit code', async () => {
    const { wrapper, program, start } = await fixture()
    writeFileSync(program, 'process.exitCode=7')
    const child = start(['runtime'])
    const exited = once(child, 'exit')
    await wrapper.entry
    await wrapper.release()
    expect(await exited).toEqual([7, null])
  })

  it('runs a pass-through launch at once and still holds the runtime launch after it', async () => {
    const { wrapper, outputFile, start } = await fixture({}, directory, { passThroughWhen: ['--probe'] })
    const probe = start(['runtime', '--probe'])
    expect(await once(probe, 'exit')).toEqual([0, null])
    expect(JSON.parse(readFileSync(outputFile, 'utf8')).argv).toEqual(['runtime', '--probe'])
    rmSync(outputFile)

    const child = start(['runtime'])
    const exited = once(child, 'exit')
    expect(await wrapper.entry).toEqual({ pid: child.pid, argv: ['runtime'] })
    expect(existsSync(outputFile)).toBe(false)
    await wrapper.release()
    expect(await exited).toEqual([0, null])
    expect(JSON.parse(readFileSync(outputFile, 'utf8')).argv).toEqual(['runtime'])
  })

  it('preserves the real exit code of a pass-through launch', async () => {
    const { start } = await fixture({}, directory, { passThroughWhen: ['--probe'] })
    const probe = start(['runtime', '--probe'], undefined, { environment: { NATIVE_WRAPPER_UNIT_EXIT: '3' } })
    expect(await once(probe, 'exit')).toEqual([3, null])
  })

  it('runs a pass-through launch with the real executable when the runtime fails', async () => {
    const { wrapper, outputFile, start } = await fixture({ failRuntime: true }, directory, { passThroughWhen: ['--probe'] })
    const probe = start(['runtime', '--probe'])
    expect(await once(probe, 'exit')).toEqual([0, null])
    expect(JSON.parse(readFileSync(outputFile, 'utf8')).argv).toEqual(['runtime', '--probe'])

    const child = start(['runtime'])
    const exited = once(child, 'exit')
    await wrapper.entry
    await wrapper.release()
    expect(await exited).toEqual([127, null])
  })

  it.each([
    { label: 'an empty word', passThroughWhen: [''] },
    { label: 'a word that is also a hold word', passThroughWhen: ['--probe', 'runtime'] },
  ])('rejects a pass-through list with $label', async ({ passThroughWhen }) => {
    await expect(fixture({}, directory, { passThroughWhen })).rejects.toThrow('pass-through')
  })

  it('reports a missing runtime executable while discovery still works', async () => {
    const { wrapper, outputFile, start } = await fixture({ failRuntime: true })
    const discovery = start(['--version'])
    expect(await once(discovery, 'exit')).toEqual([0, null])
    expect(existsSync(outputFile)).toBe(true)
    const child = start(['runtime'])
    let stderr = ''
    child.stderr?.on('data', data => stderr += String(data))
    const exited = once(child, 'exit')
    await wrapper.entry
    await wrapper.release()
    expect(await exited).toEqual([127, null])
    expect(stderr).toContain('Native startup failed:')
    expect(stderr).toContain('ENOENT')
  })

  it('rejects a wrong nonce and still accepts the real runtime handshake', async () => {
    const { wrapper, start } = await fixture()
    const socket = connect(wrapper.endpoint.port, '127.0.0.1')
    let reply = ''
    socket.on('data', data => reply += String(data))
    const closed = once(socket, 'close')
    await once(socket, 'connect')
    socket.end(`${JSON.stringify({ nonce: 'wrong-nonce', pid: process.pid, argv: ['runtime'] })}\n`)
    await closed
    expect(reply).toContain('The startup handshake was refused.')
    const child = start(['runtime'])
    const exited = once(child, 'exit')
    expect((await wrapper.entry).pid).toBe(child.pid)
    await wrapper.release()
    expect(await exited).toEqual([0, null])
  })

  it('stops a held runtime without executing the native process', async () => {
    const { wrapper, outputFile, start } = await fixture()
    const child = start(['runtime'])
    const exited = once(child, 'exit')
    await wrapper.entry
    await wrapper.dispose()
    expect(await exited).toEqual([125, null])
    expect(existsSync(outputFile)).toBe(false)
    await expect(wrapper.release()).rejects.toThrow('no longer waits for release')
  })

  it('rejects pending entry after disposal and permits repeated cleanup', async () => {
    const { wrapper } = await fixture()
    await wrapper.dispose()
    await expect(wrapper.entry).rejects.toThrow('closed before a native process entered')
    await wrapper.dispose()
  })

  it.each(['', '.', '..', 'parent/file', 'parent\\file'])('rejects an invalid executable filename: %s', async (binaryName) => {
    await expect(createNativeStartupWrapper(directory, { binaryName, executable: process.execPath })).rejects.toThrow('one executable filename')
  })
})
