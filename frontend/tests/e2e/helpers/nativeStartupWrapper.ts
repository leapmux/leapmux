import type { Buffer } from 'node:buffer'
import type { Server, Socket } from 'node:net'
import { randomUUID } from 'node:crypto'
import { chmodSync, mkdirSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:net'
import { isAbsolute, join } from 'node:path'
import process from 'node:process'
import { StringDecoder } from 'node:string_decoder'
import { isObject } from '../../../src/lib/jsonPick'
import { requireBinary } from './binaryOnPath'
import { cleanupOnFailure } from './cleanup'
import { isFileNameComponent } from './runDirectory'
import { hubSpawnEnv } from './server'

/** The provider call site supplies its executable and runtime invocation. */
export interface NativeStartupLaunch {
  binaryName: string
  executable: string
  args?: readonly string[]
  /** The wrapper holds a launch whose arguments contain EVERY one of these words. */
  holdWhen?: readonly string[]
  /**
   * The wrapper runs a launch whose arguments contain ANY of these words at once, even
   * when `holdWhen` matches it too: no hold, and no failed executable.
   *
   * For an auxiliary launch of the same binary before the runtime. The wrapper accepts
   * one handshake, so a held auxiliary launch takes it, and the wrapper then refuses the
   * real runtime launch, which exits with 125.
   */
  passThroughWhen?: readonly string[]
  lazy?: boolean
}

/**
 * Copy the pass-through words. Refuse an empty word, which states no argument, and a
 * word that is also a hold word, which would leave no launch to hold.
 */
function passThroughWords(launch: NativeStartupLaunch): string[] {
  const words = [...launch.passThroughWhen ?? []]
  if (words.includes(''))
    throw new Error('The startup wrapper pass-through words must be nonempty.')
  if (words.some(word => launch.holdWhen?.includes(word)))
    throw new Error('A startup wrapper pass-through word must not also be a hold word.')
  return words
}

/**
 * Resolve a provider's runtime invocation to the executable that a Worker started with the private agent
 * environment finds. That Worker receives `hubSpawnEnv(environment)`, and the agent environment holds no PATH
 * where it needs no change, so the lookup reads the same merged environment.
 *
 * `executableNames` lists each name that the Worker probes for the provider, in its preference order, and the
 * Worker starts the first name that its search path holds. The startup wrapper takes `launch.binaryName`, which
 * must be the first name. The Worker then starts the wrapper whichever name the search path holds, and the wrapper
 * runs the executable that this lookup finds.
 */
export function resolveNativeStartupLaunch(
  environment: Record<string, string | undefined> | undefined,
  launch: Omit<NativeStartupLaunch, 'executable'>,
  executableNames: readonly string[] = [launch.binaryName],
): NativeStartupLaunch {
  if (!environment)
    throw new Error('The native startup scenario requires the private agent environment.')
  if (!isFileNameComponent(launch.binaryName))
    throw new Error(`The native startup executable name must be one file-name component, not ${JSON.stringify(launch.binaryName)}.`)
  if (executableNames[0] !== launch.binaryName)
    throw new Error(`The startup wrapper takes ${JSON.stringify(launch.binaryName)}, so it must be the first name that the Worker probes, not ${JSON.stringify(executableNames[0])}.`)
  const executable = requireBinary(executableNames, `The isolated ${executableNames.join(' or ')} executable is absent from the PATH that the Worker receives`, hubSpawnEnv(environment))
  return { ...launch, executable }
}

export interface NativeStartupEntry {
  pid: number
  argv: string[]
  observation?: NativeStartupObservation
}

/** Actual cwd and only the environment values that the caller selected. */
export interface NativeStartupObservation {
  workingDir: string
  environment: Record<string, string | null>
}

export interface NativeStartupWrapperOptions {
  failRuntime?: boolean
  observeEnvironment?: readonly string[]
}

export interface NativeStartupWrapper {
  directory: string
  scriptPath: string
  endpoint: { port: number, nonce: string }
  entry: Promise<NativeStartupEntry>
  release: () => Promise<void>
  dispose: () => Promise<void>
}

/** Copy valid selections before a caller can change their keys. */
function observationKeys(value: unknown): string[] | undefined {
  if (value === undefined)
    return undefined
  if (!Array.isArray(value))
    throw new Error('The startup environment observation requires an array of keys.')
  const keys: string[] = []
  const seen = new Set<string>()
  for (const key of value) {
    if (typeof key !== 'string' || !/^[A-Z_]\w*$/i.test(key))
      throw new Error('The startup environment observation contains an invalid key.')
    const identity = process.platform === 'win32' ? key.toLowerCase() : key
    if (seen.has(identity))
      throw new Error('The startup environment observation contains a duplicate key.')
    seen.add(identity)
    keys.push(key)
  }
  return keys
}

/** Read one requested observation without accepting unselected environment values. */
function startupObservation(value: unknown, keys: readonly string[]): NativeStartupObservation | undefined {
  if (!isObject(value) || typeof value.workingDir !== 'string' || !isAbsolute(value.workingDir) || value.workingDir.includes('\0')
    || !isObject(value.environment) || Object.keys(value.environment).length !== keys.length) {
    return undefined
  }
  const entries: [string, string | null][] = []
  for (const key of keys) {
    if (!Object.hasOwn(value.environment, key))
      return undefined
    const entry = value.environment[key]
    if (typeof entry !== 'string' && entry !== null)
      return undefined
    entries.push([key, entry])
  }
  return { workingDir: value.workingDir, environment: Object.fromEntries(entries) }
}

/** Await listener cleanup and retain its reported error. */
function closeStartupServer(server: Server): Promise<void> {
  return new Promise<void>((resolve, reject) => server.close(error => error ? reject(error) : resolve()))
}

/** Keep both startup and listener cleanup failures. */
function failStartup(server: Server, failure: unknown): Promise<never> {
  return cleanupOnFailure(async () => {
    throw failure
  }, () => closeStartupServer(server))
}

/** Hold an actual native launch at a TCP handshake without reading its stdin. */
export async function createNativeStartupWrapper(
  directory: string,
  launch: NativeStartupLaunch,
  options: NativeStartupWrapperOptions = {},
): Promise<NativeStartupWrapper> {
  if (!isFileNameComponent(launch.binaryName))
    throw new Error('The startup wrapper requires one executable filename.')
  if (!launch.executable)
    throw new Error('The startup wrapper requires the real native executable.')
  const passThroughWhen = passThroughWords(launch)
  const selectedKeys = observationKeys(options.observeEnvironment)
  mkdirSync(directory, { recursive: true })
  const nonce = randomUUID()
  const sockets = new Set<Socket>()
  let held: Socket | undefined
  let completed = false
  let disposed = false
  let resolveEntry!: (value: NativeStartupEntry) => void
  let rejectEntry!: (error: Error) => void
  const entry = new Promise<NativeStartupEntry>((resolve, reject) => {
    resolveEntry = resolve
    rejectEntry = reject
  })
  // Disposal can precede a caller's await when Worker startup fails.
  void entry.catch(() => {})
  const server = createServer((socket) => {
    sockets.add(socket)
    socket.once('close', () => sockets.delete(socket))
    socket.on('error', () => socket.destroy())
    let input = ''
    const decoder = new StringDecoder('utf8')
    let inputBytes = 0
    const readHandshake = (data: Buffer) => {
      inputBytes += data.byteLength
      if (inputBytes > 65_536) {
        socket.destroy()
        return
      }
      input += decoder.write(data)
      const newline = input.indexOf('\n')
      if (newline < 0)
        return
      socket.off('data', readHandshake)
      let value: unknown
      try {
        value = JSON.parse(input.slice(0, newline))
      }
      catch {
        socket.end('{"error":"Invalid startup handshake."}\n')
        return
      }
      if (!isObject(value)
        || !('nonce' in value) || value.nonce !== nonce
        || !('pid' in value) || typeof value.pid !== 'number' || !Number.isSafeInteger(value.pid) || value.pid <= 0
        || !('argv' in value) || !Array.isArray(value.argv) || !value.argv.every(item => typeof item === 'string')
        || completed || disposed) {
        socket.end('{"error":"The startup handshake was refused."}\n')
        return
      }
      const observation = selectedKeys === undefined ? undefined : startupObservation(value.observation, selectedKeys)
      if (selectedKeys !== undefined && observation === undefined) {
        socket.end('{"error":"The startup environment observation was refused."}\n')
        return
      }
      completed = true
      held = socket
      resolveEntry({ pid: value.pid, argv: value.argv, ...(observation === undefined ? {} : { observation }) })
    }
    socket.on('data', readHandshake)
  })
  await new Promise<void>((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', () => {
      server.off('error', reject)
      resolve()
    })
  })
  const address = server.address()
  if (!address || typeof address === 'string')
    return failStartup(server, new Error('The startup wrapper received no TCP listener address.'))
  const endpoint = { port: address.port, nonce }
  const configuration = {
    ...endpoint,
    executable: launch.executable,
    args: [...launch.args ?? []],
    holdWhen: [...launch.holdWhen ?? []],
    passThroughWhen,
    failedExecutable: options.failRuntime ? join(directory, 'missing-native-executable') : null,
    observeEnvironment: selectedKeys ?? null,
  }
  const scriptPath = join(directory, `${launch.binaryName}.startup.cjs`)
  const source = `#!${process.execPath}
const {spawn}=require('node:child_process');
const {connect}=require('node:net');
const config=${JSON.stringify(configuration)};
const argv=process.argv.slice(2);
const runtime=!argv.some(arg=>['--version','-v','version','--help','-h','help'].includes(arg))&&config.holdWhen.every(arg=>argv.includes(arg))&&!config.passThroughWhen.some(arg=>argv.includes(arg));
let child;
function run(){
  const executable=runtime&&config.failedExecutable?config.failedExecutable:config.executable;
  child=spawn(executable,[...config.args,...argv],{stdio:'inherit',env:process.env});
  child.once('error',error=>{process.stderr.write('Native startup failed: '+error.message+'\\n');process.exitCode=127});
  child.once('exit',(code,signal)=>{if(signal){process.removeAllListeners(signal);process.kill(process.pid,signal)}else process.exitCode=code??1});
}
for(const signal of ['SIGTERM','SIGINT','SIGHUP'])process.on(signal,()=>{if(child)child.kill(signal);else process.exit(125)});
if(!runtime){run()}else{
  const socket=connect(config.port,'127.0.0.1');
  let input='';let released=false;
  socket.once('connect',()=>{
    const observation=config.observeEnvironment===null?undefined:{workingDir:process.cwd(),environment:Object.fromEntries(config.observeEnvironment.map(key=>[key,typeof process.env[key]==='string'?process.env[key]:null]))};
    socket.write(JSON.stringify({nonce:config.nonce,pid:process.pid,argv,...(observation===undefined?{}:{observation})})+'\\n');
  });
  socket.on('data',data=>{
    input+=data.toString('utf8');
    if(input.length>65536){socket.destroy();return}
    const newline=input.indexOf('\\n');if(newline<0)return;
    let value;try{value=JSON.parse(input.slice(0,newline))}catch{socket.destroy();return}
    if(value.release===true&&!released){released=true;socket.end();run()}else socket.destroy();
  });
  socket.on('error',error=>{process.stderr.write('Startup control failed: '+error.message+'\\n');process.exitCode=125});
  socket.once('close',()=>{if(!released)process.exitCode=125});
}
`
  try {
    writeFileSync(scriptPath, source)
    chmodSync(scriptPath, 0o755)
    if (process.platform === 'win32') {
      writeFileSync(join(directory, `${launch.binaryName}.cmd`), `@"${process.execPath}" "${scriptPath}" %*\r\n`)
    }
    else {
      const executable = join(directory, launch.binaryName)
      writeFileSync(executable, source)
      chmodSync(executable, 0o755)
    }
  }
  catch (error) {
    return failStartup(server, error)
  }
  return {
    directory,
    scriptPath,
    endpoint,
    entry,
    async release() {
      await entry
      if (!held || held.destroyed || disposed)
        throw new Error('The native startup process no longer waits for release.')
      const socket = held
      await new Promise<void>((resolve, reject) => {
        socket.write('{"release":true}\n', error => error ? reject(error) : resolve())
      })
    },
    async dispose() {
      if (disposed)
        return
      disposed = true
      if (!completed)
        rejectEntry(new Error('The startup wrapper closed before a native process entered.'))
      for (const socket of sockets)
        socket.destroy()
      await closeStartupServer(server)
    },
  }
}
