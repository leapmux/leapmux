import { Buffer } from 'node:buffer'
import { spawnSync } from 'node:child_process'
import { readdirSync, readFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { Writable } from 'node:stream'
import { afterEach, describe, expect, it } from 'bun:test'
import { runTestSuite } from './run-test-suite.mjs'
import { scratchDirectories } from './test-support.mjs'

const scratch = scratchDirectories()
afterEach(scratch.cleanup)

describe('runTestSuite', () => {
  it.each([0, 7])('retains complete output and the result for exit code %i', async (code) => {
    const root = scratch.create('test-suite-output')
    const output = []
    const errorOutput = []
    const sink = target => new Writable({
      write(chunk, _encoding, done) {
        target.push(chunk.toString())
        done()
      },
    })
    const args = ['-e', `process.stdout.write('stdout 日本語\\n'); process.stderr.write('stderr without newline'); process.exitCode=${code}`]
    expect(await runTestSuite('fixture', process.execPath, args, {
      root,
      stdout: sink(output),
      stderr: sink(errorOutput),
    })).toBe(code)
    const directory = join(root, '.tmp/test-results', readdirSync(join(root, '.tmp/test-results'))[0])
    const log = readFileSync(join(directory, 'console.log'), 'utf8')
    expect(log).toContain('stdout 日本語\n')
    expect(log).toContain('stderr without newline')
    expect(output.join('')).toBe('stdout 日本語\n')
    expect(errorOutput.join('')).toBe('stderr without newline')
    expect(JSON.parse(readFileSync(join(directory, 'result.json'), 'utf8'))).toMatchObject({
      label: 'fixture',
      status: code === 0
        ? 'passed'
        : 'failed',
      exitCode: code,
      signal: null,
    })
  })

  it('keeps concurrent invocations in separate directories', async () => {
    const root = scratch.create('concurrent-test-suites')
    expect(await Promise.all([0, 1].map(() => runTestSuite('fixture', process.execPath, ['-e', 'void 0'], { root })))).toEqual([0, 0])
    expect(readdirSync(join(root, '.tmp/test-results'))).toHaveLength(2)
  })

  it('drains large output through a slow console before recording completion', async () => {
    const root = scratch.create('large-test-output')
    let bytes = 0
    const stdout = new Writable({
      highWaterMark: 1,
      write(chunk, _encoding, done) {
        bytes += chunk.length
        setImmediate(done)
      },
    })
    expect(await runTestSuite('large', process.execPath, ['-e', 'process.stdout.write(Buffer.alloc(1024*1024, 65))'], { root, stdout })).toBe(0)
    expect(bytes).toBe(1024 * 1024)
    const directory = join(root, '.tmp/test-results', readdirSync(join(root, '.tmp/test-results'))[0])
    expect(readFileSync(join(directory, 'console.log'))).toEqual(Buffer.alloc(1024 * 1024, 65))
  })

  it('retains a console write failure and stops the command', async () => {
    const root = scratch.create('test-console-failure')
    const stdout = new Writable({
      write(_chunk, _encoding, done) { done(new Error('console write failure42')) },
    })
    const code = await runTestSuite('fixture', process.execPath, ['-e', 'process.stdout.write("started"); setInterval(() => {}, 1000)'], { root, stdout })
    expect(code).not.toBe(0)
    const directory = join(root, '.tmp/test-results', readdirSync(join(root, '.tmp/test-results'))[0])
    expect(readFileSync(join(directory, 'result.json'), 'utf8')).toContain('console write failure42')
  })

  it('retains a startup failure without writing environment values', async () => {
    const root = scratch.create('test-suite-start-failure')
    expect(await runTestSuite('fixture', join(root, 'missing-program'), [], {
      root,
      env: { PRIVATE_TOKEN: 'must-not-be-recorded' },
    })).toBe(1)
    const directory = join(root, '.tmp/test-results', readdirSync(join(root, '.tmp/test-results'))[0])
    const result = readFileSync(join(directory, 'result.json'), 'utf8')
    expect(JSON.parse(result)).toMatchObject({ status: 'failed', exitCode: 1 })
    expect(result).toContain('ENOENT')
    expect(result).not.toContain('must-not-be-recorded')
  })

  it.each(['', '../escape', 'two words'])('rejects an invalid suite label: %s', async (label) => {
    await expect(runTestSuite(label, process.execPath, [])).rejects.toThrow('label')
  })

  it('propagates the command failure from the executable entry point', () => {
    const root = scratch.create('test-suite-entry-point')
    const result = spawnSync(process.execPath, [
      resolve(import.meta.dirname, 'run-test-suite.mjs'),
      'fixture',
      process.execPath,
      '-e',
      'process.exitCode=9',
    ], {
      cwd: root,
      env: process.env,
      encoding: 'utf8',
    })
    expect(result.error).toBeUndefined()
    expect(result.status, result.stderr).toBe(9)
  })
})
