import { spawn } from 'node:child_process'
import { mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { describe, expect, it } from 'vitest'
import { stopProcess } from './process'
import { createToolOutputControl } from './toolOutputControl'

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
