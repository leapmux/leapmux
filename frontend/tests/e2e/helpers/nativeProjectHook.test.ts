import { spawn } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { withCleanup } from './cleanup'
import { writeNativeProjectHook } from './nativeProjectHook'
import { stopProcess } from './process'

const scratchRoot = resolve(process.cwd(), '../.tmp')
let directory: string
beforeEach(() => {
  mkdirSync(scratchRoot, { recursive: true })
  directory = mkdtempSync(join(scratchRoot, 'native-project-hook-'))
})
afterEach(() => rmSync(directory, { recursive: true, force: true }))

async function runHook(scriptPath: string, workingDirectory: string, input: string) {
  const child = spawn(process.execPath, [scriptPath], { cwd: workingDirectory, stdio: ['pipe', 'pipe', 'pipe'] })
  let stdout = ''
  let stderr = ''
  child.stdout.on('data', bytes => stdout += String(bytes))
  child.stderr.on('data', bytes => stderr += String(bytes))
  return withCleanup(async () => {
    const completion = new Promise<number | null>((resolve, reject) => {
      child.once('close', resolve)
      child.once('error', reject)
    })
    child.stdin.end(input)
    return { exitCode: await completion, stdout, stderr }
  }, () => stopProcess(child))
}

describe('writeNativeProjectHook', () => {
  it('records actual native input with zero false empty and whitespace values', async () => {
    const hook = writeNativeProjectHook(directory, 'marker')
    const input = { count: 0, enabled: false, text: '', whitespace: ' \t\n', nested: { value: null }, values: [] }
    const output = await runHook(hook.scriptPath, directory, JSON.stringify(input))
    expect(output).toEqual({ exitCode: 0, stdout: '{"continue":true}', stderr: '' })
    expect(JSON.parse(readFileSync(hook.receiptPath, 'utf8'))).toEqual({ marker: 'marker', workingDirectory: directory, input })
  })

  it('keeps quoted markers unicode and a working directory with spaces literal', async () => {
    const workingDirectory = join(directory, 'a space and unicode 한글')
    mkdirSync(workingDirectory)
    const marker = 'literal "quote" \\path $(touch expanded)\n한글'
    const hook = writeNativeProjectHook(workingDirectory, marker)
    const input = { event: 'native-event', literal: '\\"\n🙂' }
    const output = await runHook(hook.scriptPath, workingDirectory, JSON.stringify(input))
    expect(output.exitCode).toBe(0)
    expect(JSON.parse(readFileSync(hook.receiptPath, 'utf8'))).toEqual({ marker, workingDirectory, input })
    expect(existsSync(join(workingDirectory, 'expanded'))).toBe(false)
  })

  it.each(['', '{', 'null', '[]', '0', 'false', '"text"'])('rejects invalid native object input %j without a receipt', async (input) => {
    const hook = writeNativeProjectHook(directory, 'marker')
    const output = await runHook(hook.scriptPath, directory, input)
    expect(output.exitCode).toBe(1)
    expect(output.stdout).toBe('')
    expect(output.stderr).toContain('Native project hook failed:')
    expect(existsSync(hook.receiptPath)).toBe(false)
  })

  it('rejects invalid directories before creating a script', () => {
    for (const value of ['', '.', 'relative'])
      expect(() => writeNativeProjectHook(value, 'marker')).toThrow('absolute existing directory')
    const file = join(directory, 'ordinary-file')
    writeFileSync(file, 'file')
    expect(() => writeNativeProjectHook(file, 'marker')).toThrow('absolute existing directory')
    expect(existsSync(join(directory, 'native-project-hook.cjs'))).toBe(false)
  })

  it('rejects an empty marker before creating a script', () => {
    expect(() => writeNativeProjectHook(directory, '')).toThrow('nonempty marker')
    expect(existsSync(join(directory, 'native-project-hook.cjs'))).toBe(false)
  })
})
