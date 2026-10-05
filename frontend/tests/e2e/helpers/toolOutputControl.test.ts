import { spawn } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { describe, expect, it } from 'vitest'
import { COLLAPSED_LINE_CHAR_CAP } from '../../../src/components/chat/results/useCollapsedLines'
import { stopProcess } from './process'
import { createToolOutputControl, waitForFileSignal } from './toolOutputControl'

const SCRATCH_ROOT = resolve(process.cwd(), '../.tmp')

describe('createToolOutputControl', () => {
  it('holds each output segment until its explicit release', async () => {
    mkdirSync(SCRATCH_ROOT, { recursive: true })
    const directory = mkdtempSync(join(SCRATCH_ROOT, 'output-control-test-'))
    const control = createToolOutputControl(directory)
    const child = spawn(globalThis.process.execPath, [control.scriptPath])
    const output: string[] = []
    child.stdout.on('data', chunk => output.push(String(chunk)))
    const exited = new Promise<number | null>((resolve, reject) => {
      child.once('exit', resolve)
      child.once('error', reject)
    })
    try {
      await control.waitForFirstOutput()
      await expect.poll(() => output.join('')).toContain(control.firstMarker)
      expect(output.join('')).not.toContain(control.secondMarker)
      expect(child.exitCode).toBeNull()
      await control.releaseFirstOutput()
      await control.waitForSecondOutput()
      await expect.poll(() => output.join('')).toContain(control.secondMarker)
      expect(child.exitCode).toBeNull()
      await control.releaseFinalOutput()
      expect(await exited).toBe(0)
    }
    finally {
      await stopProcess(child)
      rmSync(directory, { recursive: true, force: true })
    }
  })

  // Codex runs the command in its macOS seatbelt sandbox. There `fs.watch` emits
  // an asynchronous EMFILE error ("too many open files, watch"), which is what
  // this preload reproduces. A command that depends on `fs.watch` then ends with
  // exit code 1 straight after its first output, so no spec can hold it.
  it('holds its segments without a file watch', async () => {
    mkdirSync(SCRATCH_ROOT, { recursive: true })
    const directory = mkdtempSync(join(SCRATCH_ROOT, 'output-control-no-watch-'))
    const control = createToolOutputControl(directory)
    const preload = join(directory, 'watch-fails.cjs')
    writeFileSync(preload, `
const fs = require('node:fs')
const { EventEmitter } = require('node:events')
fs.watch = () => {
  const watcher = new EventEmitter()
  watcher.close = () => {}
  setImmediate(() => watcher.emit('error', Object.assign(new Error('EMFILE: too many open files, watch'), { code: 'EMFILE' })))
  return watcher
}
`)
    const child = spawn(globalThis.process.execPath, ['--require', preload, control.scriptPath])
    const output: string[] = []
    child.stdout.on('data', chunk => output.push(String(chunk)))
    const exited = new Promise<number | null>((resolve, reject) => {
      child.once('exit', resolve)
      child.once('error', reject)
    })
    try {
      await control.waitForFirstOutput()
      await control.releaseFirstOutput()
      await expect.poll(() => output.join('').includes(control.secondMarker) || child.exitCode !== null).toBe(true)
      expect(child.exitCode, 'the command must still run after its second output').toBeNull()
      expect(output.join('')).toContain(control.secondMarker)
      await control.releaseFinalOutput()
      expect(await exited).toBe(0)
    }
    finally {
      await stopProcess(child)
      rmSync(directory, { recursive: true, force: true })
    }
  })

  it('keeps the first output back until its start release', async () => {
    mkdirSync(SCRATCH_ROOT, { recursive: true })
    const directory = mkdtempSync(join(SCRATCH_ROOT, 'output-control-hold-start-'))
    const control = createToolOutputControl(directory, undefined, { holdFirstOutput: true })
    // The preload states when the command looked for its start release twice
    // and found none. That is the proof that the command ran and held back,
    // with no wait for a fixed time.
    const checked = join(directory, 'start-checked')
    const preload = join(directory, 'count-start-checks.cjs')
    writeFileSync(preload, `
const fs = require('node:fs')
const exists = fs.existsSync
let missing = 0
fs.existsSync = (path) => {
  const found = exists(path)
  if (String(path).endsWith('release-start') && !found && ++missing === 2)
    fs.writeFileSync(${JSON.stringify(checked)}, 'checked')
  return found
}
`)
    const child = spawn(globalThis.process.execPath, ['--require', preload, control.scriptPath])
    const output: string[] = []
    child.stdout.on('data', chunk => output.push(String(chunk)))
    const exited = new Promise<number | null>((resolve, reject) => {
      child.once('exit', resolve)
      child.once('error', reject)
    })
    try {
      await waitForFileSignal(checked)
      expect(existsSync(join(directory, 'first-ready')), 'the command held its first output').toBe(false)
      expect(output.join('')).toBe('')

      await control.releaseStartOutput()
      await control.waitForFirstOutput()
      await expect.poll(() => output.join('')).toContain(control.firstMarker)
      expect(output.join('')).not.toContain(control.secondMarker)
      expect(child.exitCode).toBeNull()
      await control.releaseFirstOutput()
      await control.waitForSecondOutput()
      await expect.poll(() => output.join('')).toContain(control.secondMarker)
      await control.releaseFinalOutput()
      expect(await exited).toBe(0)
    }
    finally {
      await stopProcess(child)
      rmSync(directory, { recursive: true, force: true })
    }
  })

  it('writes the segments in order when every release precedes the command', async () => {
    mkdirSync(SCRATCH_ROOT, { recursive: true })
    const directory = mkdtempSync(join(SCRATCH_ROOT, 'output-control-hold-early-'))
    const control = createToolOutputControl(directory, undefined, { holdFirstOutput: true })
    await control.releaseFirstOutput()
    await control.releaseFinalOutput()
    await control.releaseStartOutput()
    const child = spawn(globalThis.process.execPath, [control.scriptPath])
    const output: string[] = []
    child.stdout.on('data', chunk => output.push(String(chunk)))
    try {
      const exitCode = await new Promise<number | null>((resolve, reject) => {
        child.once('exit', resolve)
        child.once('error', reject)
      })
      expect(exitCode).toBe(0)
      const text = output.join('')
      expect(text).toContain(control.firstMarker)
      expect(text.indexOf(control.secondMarker)).toBeGreaterThan(text.indexOf(control.firstMarker))
    }
    finally {
      await stopProcess(child)
      rmSync(directory, { recursive: true, force: true })
    }
  })

  it('accepts release signals written before the command starts', async () => {
    mkdirSync(SCRATCH_ROOT, { recursive: true })
    const directory = mkdtempSync(join(SCRATCH_ROOT, 'output-control-early-'))
    const control = createToolOutputControl(directory)
    await control.releaseFirstOutput()
    await control.releaseFinalOutput()
    const child = spawn(globalThis.process.execPath, [control.scriptPath])
    const output: string[] = []
    child.stdout.on('data', chunk => output.push(String(chunk)))
    try {
      const exitCode = await new Promise<number | null>((resolve, reject) => {
        child.once('exit', resolve)
        child.once('error', reject)
      })
      expect(exitCode).toBe(0)
      expect(output.join('')).toContain(control.firstMarker)
      expect(output.join('')).toContain(control.secondMarker)
    }
    finally {
      await stopProcess(child)
      rmSync(directory, { recursive: true, force: true })
    }
  })

  it('ends each segment with its live tail, away from the other segment', async () => {
    mkdirSync(SCRATCH_ROOT, { recursive: true })
    const directory = mkdtempSync(join(SCRATCH_ROOT, 'output-control-live-tail-'))
    const control = createToolOutputControl(directory)
    const child = spawn(globalThis.process.execPath, [control.scriptPath])
    const output: string[] = []
    child.stdout.on('data', chunk => output.push(String(chunk)))
    try {
      await control.waitForFirstOutput()
      await expect.poll(() => output.join('').endsWith(`${control.firstLiveTail}\n`)).toBe(true)
      const first = output.join('')
      expect(first.startsWith(`${control.firstMarker}\n`)).toBe(true)
      expect(first).not.toContain(control.secondLiveTail)

      await control.releaseFirstOutput()
      await control.waitForSecondOutput()
      await expect.poll(() => output.join('').endsWith(`${control.secondLiveTail}\n`)).toBe(true)
      const second = output.join('').slice(first.length)
      expect(second.startsWith(`${control.secondMarker}\n`)).toBe(true)
      expect(second).not.toContain(control.firstLiveTail)
      await control.releaseFinalOutput()
    }
    finally {
      await stopProcess(child)
      rmSync(directory, { recursive: true, force: true })
    }
  })

  it('keeps each live tail inside one collapsed line', () => {
    mkdirSync(SCRATCH_ROOT, { recursive: true })
    const directory = mkdtempSync(join(SCRATCH_ROOT, 'output-control-live-tail-cap-'))
    try {
      const control = createToolOutputControl(directory)
      for (const tail of [control.firstLiveTail, control.secondLiveTail]) {
        expect(tail.length).toBeGreaterThan(0)
        expect(tail.length).toBeLessThan(COLLAPSED_LINE_CHAR_CAP)
      }
      expect(control.firstLiveTail).not.toBe(control.secondLiveTail)
    }
    finally {
      rmSync(directory, { recursive: true, force: true })
    }
  })

  it('rejects an empty or relative working directory', () => {
    expect(() => createToolOutputControl('')).toThrow('absolute working directory')
    expect(() => createToolOutputControl('relative')).toThrow('absolute working directory')
  })

  it('keeps supplied output markers literal through the native command', async () => {
    mkdirSync(SCRATCH_ROOT, { recursive: true })
    const directory = mkdtempSync(join(SCRATCH_ROOT, 'output-control-markers-'))
    const markers = { first: 'LEAPMUX OUTPUT two "quoted"', second: 'LEAPMUX OUTPUT eight \\path' }
    const control = createToolOutputControl(directory, markers)
    await control.releaseFirstOutput()
    await control.releaseFinalOutput()
    const child = spawn(process.execPath, [control.scriptPath])
    const output: string[] = []
    child.stdout.on('data', chunk => output.push(String(chunk)))
    try {
      const code = await new Promise<number | null>((resolve, reject) => {
        child.once('exit', resolve)
        child.once('error', reject)
      })
      expect(code).toBe(0)
      expect(output.join('')).toContain(`${markers.first}\n`)
      expect(output.join('')).toContain(`${markers.second}\n`)
    }
    finally {
      await stopProcess(child)
      rmSync(directory, { recursive: true, force: true })
    }
  })

  it('rejects empty or repeated output markers before a command starts', () => {
    mkdirSync(SCRATCH_ROOT, { recursive: true })
    const directory = mkdtempSync(join(SCRATCH_ROOT, 'output-control-invalid-markers-'))
    try {
      for (const markers of [{ first: '', second: 'second' }, { first: 'first', second: '' }, { first: 'same', second: 'same' }])
        expect(() => createToolOutputControl(directory, markers)).toThrow('distinct nonempty markers')
    }
    finally {
      rmSync(directory, { recursive: true, force: true })
    }
  })
})
