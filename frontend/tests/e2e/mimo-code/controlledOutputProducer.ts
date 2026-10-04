import type { NativeToolOutput } from '../helpers/nativeToolOutput'
import { existsSync, lstatSync, mkdirSync, readdirSync, realpathSync, watch, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { quotePosixShellArgument } from '../helpers/shellArguments'

export interface MiMoProducerFile {
  path: string
  byteLength: number
  identity: string
}

export interface MiMoOutputSizePorts {
  files: () => MiMoProducerFile[]
  subscribe: (changed: () => void, failed: (error: unknown) => void) => () => void
  subscribeFile: (path: string, changed: () => void, failed: (error: unknown) => void) => () => void
  release: () => void
}

export interface MiMoOutputSizeDiagnostics {
  expectedBytes: number
  finished: boolean
  released: boolean
  inspections: number
  lastFiles: (MiMoProducerFile & { prior: boolean })[]
  failureMessage: string | undefined
}

/** Hold the producer until a new native output file reaches the expected size. */
export function observeMiMoNativeOutputSize(expected: string, previous: ReadonlySet<string>, ports: MiMoOutputSizePorts): { observedPath: Promise<string>, diagnostic: () => MiMoOutputSizeDiagnostics, close: () => void } {
  const maximum = new TextEncoder().encode(expected).byteLength
  if (!expected || maximum > 4 * 1024 * 1024)
    throw new Error('The controlled MiMo producer requires nonempty expected output within four MiB.')
  let resolve!: (path: string) => void
  let reject!: (error: unknown) => void
  const observedPath = new Promise<string>((accept, refuse) => {
    resolve = accept
    reject = refuse
  })
  let finished = false
  let released = false
  let inspections = 0
  let lastFiles: MiMoProducerFile[] = []
  let failureMessage: string | undefined
  let directoryStop: (() => void) | undefined
  let inspecting = false
  let inspectionRequested = false
  const lateCloseFailures: unknown[] = []
  const subscriptions = new Map<string, { identity: string, stop?: () => void }>()
  const release = () => {
    if (released)
      return
    ports.release()
    released = true
  }
  const closeSubscriptions = () => {
    const failures: unknown[] = []
    const stops = [...subscriptions.values()].flatMap(value => value.stop ? [value.stop] : [])
    subscriptions.clear()
    if (directoryStop) {
      stops.push(directoryStop)
      directoryStop = undefined
    }
    for (const stop of stops) {
      try {
        stop()
      }
      catch (error) {
        failures.push(error)
      }
    }
    if (failures.length)
      throw new AggregateError(failures, 'The native MiMo observers failed to close.')
  }
  const fail = (error: unknown) => {
    if (finished)
      return
    finished = true
    let failure = error
    try {
      closeSubscriptions()
    }
    catch (closeError) {
      failure = new AggregateError([error, closeError], 'The native MiMo observation and cleanup failed.')
    }
    failureMessage = failure instanceof Error ? failure.message : String(failure)
    reject(failure)
  }
  const closeLateSubscription = (stop: () => void) => {
    try {
      stop()
    }
    catch (error) {
      lateCloseFailures.push(error)
    }
  }
  const inspect = () => {
    if (finished)
      return
    if (inspecting) {
      inspectionRequested = true
      return
    }
    inspecting = true
    try {
      inspections++
      let files = ports.files().map(file => ({ ...file }))
      for (const [path, subscription] of subscriptions) {
        const current = files.find(file => file.path === path)
        if (!current || current.identity !== subscription.identity) {
          subscriptions.delete(path)
          subscription.stop?.()
        }
      }
      let subscribed = false
      for (const file of files) {
        if (previous.has(file.path) || !file.identity || !Number.isSafeInteger(file.byteLength) || file.byteLength < 0 || subscriptions.has(file.path))
          continue
        const subscription: { identity: string, stop?: () => void } = { identity: file.identity }
        subscriptions.set(file.path, subscription)
        const stop = ports.subscribeFile(file.path, inspect, fail)
        if (finished || subscriptions.get(file.path) !== subscription) {
          closeLateSubscription(stop)
          return
        }
        subscription.stop = stop
        subscribed = true
      }
      // Re-read after subscription so a final write during installation remains visible.
      if (subscribed)
        files = ports.files().map(file => ({ ...file }))
      lastFiles = files
      for (const file of files) {
        if (previous.has(file.path))
          continue
        const subscription = subscriptions.get(file.path)
        if (!subscription)
          continue
        if (subscription.identity !== file.identity)
          throw new Error('The native MiMo file changed during observer installation.')
        if (file.byteLength !== maximum)
          continue
        if (finished)
          return
        closeSubscriptions()
        if (finished)
          return
        release()
        finished = true
        resolve(file.path)
        return
      }
    }
    catch (error) {
      fail(error)
    }
    finally {
      inspecting = false
      if (inspectionRequested && !finished) {
        inspectionRequested = false
        queueMicrotask(inspect)
      }
    }
  }
  try {
    directoryStop = ports.subscribe(inspect, fail)
    if (finished) {
      closeLateSubscription(directoryStop)
      directoryStop = undefined
    }
    else {
      inspect()
    }
  }
  catch (error) {
    fail(error)
  }
  return {
    observedPath,
    diagnostic: () => ({
      expectedBytes: maximum,
      finished,
      released,
      inspections,
      lastFiles: lastFiles.map(file => ({ ...file, prior: previous.has(file.path) })),
      failureMessage,
    }),
    close: () => {
      if (!finished)
        fail(new Error('The controlled MiMo producer ended before native file-size observation.'))
      else
        closeSubscriptions()
      release()
      if (lateCloseFailures.length)
        throw new AggregateError(lateCloseFailures, 'The late native MiMo observers failed to close.')
    },
  }
}

/** Observe native BashTool file writes before the process ends and cancels the stdout reader. */
export function controlledMiMoOutputFileProducer(directory: string, releaseFile: string, output: NativeToolOutput) {
  mkdirSync(directory, { recursive: true })
  if (existsSync(releaseFile))
    throw new Error('The controlled MiMo producer requires a new private release file.')
  const files = () => readdirSync(directory).filter(name => /^tool_[A-Za-z0-9]+$/.test(name)).map((name) => {
    const path = join(directory, name)
    const info = lstatSync(path)
    return { path, byteLength: info.isFile() && !info.isSymbolicLink() ? info.size : -1, identity: `${info.dev}:${info.ino}` }
  })
  const initialDirectory = lstatSync(directory)
  const previous = new Set(files().map(file => file.path))
  const notifications: { scope: 'directory' | 'file', path: string, kind: string, filename: string | null }[] = []
  let notificationCount = 0
  const control = observeMiMoNativeOutputSize(output.text, previous, {
    files,
    subscribe: (changed, failed) => {
      const watcher = watch(directory, (kind, filename) => {
        notificationCount++
        if (notifications.length < 100)
          notifications.push({ scope: 'directory', path: directory, kind, filename: filename === null ? null : filename.toString() })
        changed()
      })
      watcher.on('error', failed)
      return () => watcher.close()
    },
    subscribeFile: (path, changed, failed) => {
      const watcher = watch(path, (kind, filename) => {
        notificationCount++
        if (notifications.length < 100)
          notifications.push({ scope: 'file', path, kind, filename: filename === null ? null : filename.toString() })
        changed()
      })
      watcher.on('error', failed)
      return () => watcher.close()
    },
    release: () => writeFileSync(releaseFile, 'release', { flag: 'wx' }),
  })
  const source = `(async () => {\n${output.source}\nconst fs = require('node:fs');\nprocess.stdout.write(completeOutput);\nawait new Promise(resolve => { const ready = () => fs.existsSync(${JSON.stringify(releaseFile)}); const watcher = fs.watch(${JSON.stringify(dirname(releaseFile))}, () => { if (ready()) { watcher.close(); resolve(); } }); if (ready()) { watcher.close(); resolve(); } });\n})().catch(error => { console.error(error); process.exitCode = 1; });`
  const diagnostic = () => {
    const describeDirectory = () => {
      try {
        const current = lstatSync(directory)
        return { path: directory, realpath: realpathSync(directory), initialDevice: initialDirectory.dev, initialInode: initialDirectory.ino, currentDevice: current.dev, currentInode: current.ino }
      }
      catch (error) {
        return { path: directory, failure: error instanceof Error ? error.message : String(error) }
      }
    }
    let currentFiles: object[]
    try {
      currentFiles = readdirSync(directory).map((name) => {
        const path = join(directory, name)
        const info = lstatSync(path)
        return { path, bytes: info.size, identity: `${info.dev}:${info.ino}`, selected: /^tool_[A-Za-z0-9]+$/.test(name), prior: previous.has(path), regular: info.isFile() && !info.isSymbolicLink() }
      })
    }
    catch (error) {
      currentFiles = [{ failure: error instanceof Error ? error.message : String(error) }]
    }
    return { observer: control.diagnostic(), notificationCount, notifications, directory: describeDirectory(), releaseFile, releaseFileExists: existsSync(releaseFile), currentFiles }
  }
  return { ...control, diagnostic, command: `node -e ${quotePosixShellArgument(source)}` }
}
