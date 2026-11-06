import { spawnSync } from 'node:child_process'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { delimiter, join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, describe, expect, it } from 'bun:test'
import { GO_TOOL_MODULES } from './go-tools.mjs'
import { runGoTool } from './run-go-tool.mjs'
import { scratchDirectories } from './test-support.mjs'

const scratch = scratchDirectories()
afterEach(scratch.cleanup)

function fixture(results = []) {
  const calls = []
  return {
    calls,
    run: (command, args, options) => {
      calls.push({ command, args, options })
      return results.shift() ?? { status: 0, stdout: '' }
    },
  }
}

const host = {
  status: 0,
  stdout: JSON.stringify({ GOHOSTOS: 'linux', GOHOSTARCH: 'arm64', GOROOT: '/go root' }),
}
const built = { status: 0, stdout: '/cache with spaces/tool\n' }

describe('runGoTool', () => {
  it('builds a real tool outside an unusable application workspace and runs it in the caller directory', () => {
    const checkout = scratch.create('isolated tool with spaces')
    const toolDirectory = resolve(checkout, 'tools/gotestsum')
    const cwd = resolve(checkout, 'staging')
    mkdirSync(toolDirectory, { recursive: true })
    mkdirSync(cwd)
    writeFileSync(resolve(checkout, 'go.work'), 'this is not a valid workspace\n')
    const goEnv = resolve(checkout, 'go.env')
    writeFileSync(goEnv, 'GOFLAGS=-modfile=missing-persisted.mod\n')
    const version = /^go (.+)$/m.exec(readFileSync(resolve(import.meta.dirname, '../go.work'), 'utf8'))[1]
    writeFileSync(resolve(toolDirectory, 'go.mod'), `module example.com/gotestsum\ngo ${version}\ntool example.com/gotestsum\n`)
    writeFileSync(resolve(toolDirectory, 'main.go'), `package main
import ("encoding/json"; "os")
func main() {
  cwd, err := os.Getwd(); if err != nil { panic(err) }
  if err := json.NewEncoder(os.Stdout).Encode([]any{cwd, os.Args[1:], os.Getenv("GOWORK"), os.Getenv("GOFLAGS")}); err != nil { panic(err) }
}
`)
    const env = {
      ...process.env,
      GOENV: goEnv,
      GOWORK: resolve(checkout, 'go.work'),
      GOFLAGS: '-modfile=missing.mod',
      GOPROXY: 'off',
      GOTOOLCHAIN: 'local',
      GOOS: process.platform === 'win32'
        ? 'linux'
        : 'windows',
      GOARCH: 'amd64',
    }
    let output
    const status = runGoTool('gotestsum', ['space here', '$(literal)'], {
      checkout,
      cwd,
      env,
      run: (command, args, options) => {
        const result = spawnSync(command, args, {
          ...options,
          encoding: 'utf8',
          stdio: ['ignore', 'pipe', 'pipe'],
        })
        if (command !== 'go')
          output = result.stdout
        expect(result.error, result.stderr).toBeUndefined()
        expect(result.status, result.stderr).toBe(0)
        return result
      },
    })
    expect(status).toBe(0)
    expect(JSON.parse(output)).toEqual([
      cwd,
      ['space here', '$(literal)'],
      env.GOWORK,
      env.GOFLAGS,
    ])
  })

  it('keeps each tool in its own module outside the application workspace', () => {
    const root = resolve(import.meta.dirname, '..')
    const workspace = readFileSync(resolve(root, 'go.work'), 'utf8')
    for (const file of GO_TOOL_MODULES) {
      const module = readFileSync(resolve(root, file), 'utf8')
      expect(module.match(/^tool /gm)).toHaveLength(1)
      expect(workspace).not.toContain(file.replace('/go.mod', ''))
    }
    for (const file of ['backend/go.mod', 'desktop/go/go.mod'])
      expect(readFileSync(resolve(root, file), 'utf8')).not.toMatch(/^tool /m)
  })
  it('isolates compilation and preserves the caller context and literal arguments during execution', () => {
    const context = fixture([
      host,
      built,
      { status: 7 },
    ])
    const env = {
      GOWORK: '/app/go.work',
      GOOS: 'windows',
      GOARCH: 'amd64',
      GOFLAGS: '-modfile=app.mod',
      PATH: '/bin',
      TOKEN: 'kept',
    }
    expect(runGoTool('sqlc', ['generate', 'a b', '$(literal)'], {
      checkout: '/checkout',
      cwd: '/staging directory',
      env,
      run: context.run,
    })).toBe(7)
    expect(context.calls[0].options.cwd).toBe(resolve('/checkout/tools/sqlc'))
    expect(context.calls[0].options.env.GOWORK).toBe('off')
    expect(context.calls[1].args).toEqual(['tool', '-n', 'sqlc'])
    expect(context.calls[1].options.env).toMatchObject({
      GOWORK: 'off',
      GOOS: 'linux',
      GOARCH: 'arm64',
      GOFLAGS: '-mod=readonly',
    })
    expect(context.calls[2]).toEqual({
      command: '/cache with spaces/tool',
      args: ['generate', 'a b', '$(literal)'],
      options: {
        cwd: '/staging directory',
        env: { ...env, PATH: `${join('/go root', 'bin')}${delimiter}/bin` },
        stdio: 'inherit',
      },
    })
    expect(env.PATH).toBe('/bin')
  })

  it('rejects missing and unknown tools before starting Go', () => {
    const context = fixture()
    for (const name of [undefined, '', '../sqlc', 'unknown'])
      expect(() => runGoTool(name, [], { run: context.run })).toThrow('Unknown Go tool')
    expect(context.calls).toHaveLength(0)
  })

  it('stops after host discovery or compilation fails', () => {
    for (const results of [
      [
        { status: 3 },
      ],
      [
        host,
        { status: 4 },
      ],
    ]) {
      const expectedCalls = results.length
      const expectedStatus = results.at(-1).status
      const context = fixture([...results])
      expect(runGoTool('gotestsum', [], { run: context.run })).toBe(expectedStatus)
      expect(context.calls).toHaveLength(expectedCalls)
    }
  })

  it('reports spawn failures and signal termination at every stage', () => {
    for (const prefix of [
      [],
      [host],
      [host, built],
    ]) {
      expect(() => runGoTool('gotestsum', [], { run: fixture([
        ...prefix,
        {
          error: new Error('spawn failed'),
        },
      ]).run })).toThrow('spawn failed')
      expect(runGoTool('gotestsum', [], { run: fixture([
        ...prefix,
        { status: null, signal: 'SIGTERM' },
      ]).run })).toBe(143)
    }
  })

  it('rejects absent or malformed executable paths', () => {
    for (const stdout of ['', 'relative/tool', '/first\n/second']) {
      expect(() => runGoTool('sqlc', [], { run: fixture([
        host,
        { status: 0, stdout },
      ]).run })).toThrow('invalid executable path')
    }
  })
})
