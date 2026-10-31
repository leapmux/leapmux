import { existsSync, readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'bun:test'
import { runCommand, runGoOutsideModules } from './renovate-go-command.mjs'

const root = resolve(import.meta.dirname, '..')

describe('runGoOutsideModules', () => {
  it('runs go in a scratch directory with its files, outside every workspace, and removes the directory', async () => {
    let seen
    const result = await runGoOutsideModules(['mod', 'edit', '-json', 'go.mod'], {
      checkout: root,
      env: { HOME: '/home/fixture', GOWORK: 'on', GOTOOLCHAIN: 'auto' },
      files: { 'go.mod': 'module example.com/app\n' },
      run: async (command, args, options) => {
        seen = {
          command,
          args,
          env: options.env,
          inTmp: options.cwd.startsWith(resolve(root, '.tmp/go-')),
          file: readFileSync(resolve(options.cwd, 'go.mod'), 'utf8'),
          cwd: options.cwd,
        }
        return { status: 0, stdout: 'ok', stderr: '' }
      },
    })
    expect(result).toEqual({ status: 0, stdout: 'ok', stderr: '' })
    expect(seen).toMatchObject({
      command: 'go',
      args: ['mod', 'edit', '-json', 'go.mod'],
      env: { HOME: '/home/fixture', GOWORK: 'off', GOTOOLCHAIN: 'local' },
      inTmp: true,
      file: 'module example.com/app\n',
    })
    expect(existsSync(seen.cwd)).toBe(false)
  })
  it('removes the scratch directory when the command fails to run', async () => {
    let directory
    await expect(runGoOutsideModules(['version'], {
      checkout: root,
      run: async (command, args, options) => {
        directory = options.cwd
        throw new Error('spawn failed')
      },
    })).rejects.toThrow('spawn failed')
    expect(existsSync(directory)).toBe(false)
  })
})

describe('runCommand', () => {
  it('collects the output and the status, and reports a command that cannot start', async () => {
    expect(await runCommand(process.execPath, ['-e', 'process.stdout.write("out"); process.stderr.write("err"); process.exit(3)'], {})).toEqual({ status: 3, stdout: 'out', stderr: 'err' })
    const missing = await runCommand('leapmux-missing-command', [], {})
    expect(missing.status).toBeNull()
    expect(missing.error).toBeInstanceOf(Error)
  })
})
