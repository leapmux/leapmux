import { execFileSync, spawnSync } from 'node:child_process'
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { describe, expect, it } from 'vitest'
import { workerRegistrationCommands } from './workerRegistrationCommands'

describe('workerRegistrationCommands', () => {
  it.each([
    '\'',
    '\u2018',
    '\u2019',
    '\u201A',
    '\u201B',
  ])('escapes the PowerShell single-quote delimiter %s in both arguments', (quote) => {
    const value = `literal${quote} ; Write-Output marker; #`
    const command = workerRegistrationCommands({ hubUrl: value, registrationKey: value, shell: 'powershell' })
    const expected = `literal${quote}${quote} ; Write-Output marker; #`
    expect(command?.register).toContain(`--hub "${expected}" --registration-key "${expected}"`)
  })

  const literals = `space ' apostrophe " quote $dollar $(substitution) \u0060tick\u0060 & | ; < > * ? [x] \\ slash Ω 😀`

  it.skipIf(process.platform === 'win32')('preserves literal arguments through a POSIX shell', () => {
    const command = workerRegistrationCommands({ hubUrl: literals, registrationKey: literals, shell: 'posix' })!
    const output = execFileSync('/bin/sh', ['-c', `leapmux() { printf '%s\\0' "$@"; }; ${command.register}`], { encoding: 'utf8' })
    expect(output.split('\0')).toEqual([
      'worker',
      'register',
      '--hub',
      literals,
      '--registration-key',
      literals,
      '',
    ])
  })

  const runtimes = process.platform === 'win32'
    ? ['powershell.exe', 'pwsh.exe']
    : ['pwsh']
  for (const runtime of runtimes) {
    const probe = spawnSync(runtime, [
      '-NoProfile',
      '-NonInteractive',
      '-Command',
      '(Get-Process -Id $PID).Path',
    ], { encoding: 'utf8' })
    const executable = probe.status === 0
      ? probe.stdout.trim()
      : null
    const missingRuntime = (probe.error as NodeJS.ErrnoException | undefined)?.code === 'ENOENT'
    for (const mode of ['Legacy', 'Standard']) {
      it.skipIf(missingRuntime)(`preserves external process arguments through ${runtime} in ${mode} mode`, () => {
        expect(probe.error).toBeUndefined()
        expect(probe.status, probe.stderr).toBe(0)
        expect(executable).toBeTruthy()
        // Task runs Vitest from frontend. The DOM runner rewrites import.meta.url.
        const scratch = resolve(process.cwd(), '../.tmp')
        mkdirSync(scratch, { recursive: true })
        const directory = mkdtempSync(join(scratch, 'registration-argv-'))
        try {
          const binary = join(directory, process.platform === 'win32'
            ? 'leapmux.exe'
            : 'leapmux')
          copyFileSync(process.execPath, binary)
          writeFileSync(join(directory, 'package.json'), '{"type":"commonjs"}')
          writeFileSync(join(directory, 'worker'), `const fs = require('node:fs'); const path = require('node:path'); fs.appendFileSync(process.env.LEAPMUX_ARGV_OUTPUT, JSON.stringify([path.basename(process.argv[1]), ...process.argv.slice(2)]) + '\\n'); process.stderr.write('external receiver diagnostic\\n'); process.exitCode = Number(process.env.LEAPMUX_ARGV_EXIT || 0);`)
          const output = join(directory, 'arguments.txt')
          const exitOutput = join(directory, 'exit.txt')
          const environment: NodeJS.ProcessEnv = {
            ...process.env,
            LEAPMUX_ARGV_OUTPUT: output,
            LEAPMUX_ARGV_EXIT: '0',
            LEAPMUX_EXIT_OUTPUT: exitOutput,
          }
          for (const key of Object.keys(environment)) {
            if (key.toUpperCase() === 'PATH')
              delete environment[key]
          }
          Object.assign(environment, { PATH: directory })
          const values = [
            literals,
            'embedded"quote',
            'slash\\"quote',
            'two\\\\"quotes"',
            'trailing\\',
            'trailing\\\\',
            '"',
            '\\',
            ...['\u2018', '\u2019', '\u201A', '\u201B'].map(quote => `literal${quote} ; Write-Output injected_marker; #`),
          ]
          const expected: string[][] = []
          const commands: string[] = []
          for (const value of values) {
            for (const field of ['hubUrl', 'registrationKey'] as const) {
              const inputs = {
                hubUrl: 'https://hub.example',
                registrationKey: 'key',
                shell: 'powershell' as const,
                [field]: value,
              }
              commands.push(workerRegistrationCommands(inputs)!.register)
              expected.push([
                'worker',
                'register',
                '--hub',
                inputs.hubUrl,
                '--registration-key',
                inputs.registrationKey,
              ])
            }
          }
          const result = spawnSync(executable!, [
            '-NoProfile',
            '-NonInteractive',
            '-Command',
            `$PSNativeCommandArgumentPassing = '${mode}'; ${commands.join('; ')}; if ($PSNativeCommandArgumentPassing -ne '${mode}') { throw 'native passing mode changed' }`,
          ], { cwd: directory, env: environment, encoding: 'utf8' })
          expect(result.error).toBeUndefined()
          expect(result.status, result.stderr).toBe(0)
          expect(result.stdout).not.toContain('injected_marker')
          expect(readFileSync(output, 'utf8').trim().split('\n').map(line => JSON.parse(line))).toEqual(expected)

          const command = workerRegistrationCommands({ hubUrl: literals, registrationKey: literals, shell: 'powershell' })!.register
          const failed = spawnSync(executable!, [
            '-NoProfile',
            '-NonInteractive',
            '-Command',
            `try { ${command} } catch { [IO.File]::WriteAllText($env:LEAPMUX_EXIT_OUTPUT, [string]$LASTEXITCODE); throw }`,
          ], {
            cwd: directory,
            env: { ...environment, LEAPMUX_ARGV_EXIT: '17' },
            encoding: 'utf8',
          })
          expect(failed.error).toBeUndefined()
          expect(failed.status).not.toBe(0)
          expect(failed.stderr).toContain('external receiver diagnostic')
          expect(readFileSync(exitOutput, 'utf8')).toBe('17')

          rmSync(binary)
          rmSync(output)
          const missing = spawnSync(executable!, [
            '-NoProfile',
            '-NonInteractive',
            '-Command',
            command,
          ], { cwd: directory, env: environment, encoding: 'utf8' })
          expect(missing.error).toBeUndefined()
          expect(missing.status).not.toBe(0)
          expect(missing.stderr).toContain('leapmux')
          expect(() => readFileSync(output)).toThrow(expect.objectContaining({ code: 'ENOENT' }))
        }
        finally {
          rmSync(directory, { recursive: true, force: true })
        }
      })
    }
  }

  it('refuses every C0 character and DEL in either argument for both shells', () => {
    for (const shell of ['posix', 'powershell'] as const) {
      for (const code of [
        ...Array.from({ length: 32 }, (_, index) => index),
        127,
      ]) {
        const unsafe = `before${String.fromCharCode(code)}after`
        expect(workerRegistrationCommands({ hubUrl: unsafe, registrationKey: 'key', shell }), `${shell} Hub ${code}`).toBeNull()
        expect(workerRegistrationCommands({ hubUrl: 'https://hub.example', registrationKey: unsafe, shell }), `${shell} key ${code}`).toBeNull()
      }
    }
  })
  it('quotes each argument for the selected shell', () => {
    const posix = workerRegistrationCommands({
      hubUrl: 'https://hub.example/path',
      registrationKey: 'key with spaces',
      shell: 'posix',
    })
    expect(posix?.register).toBe(`leapmux worker register --hub 'https://hub.example/path' --registration-key 'key with spaces'`)

    const powershell = workerRegistrationCommands({
      hubUrl: 'https://hub.example/path',
      registrationKey: 'it\'s a key',
      shell: 'powershell',
    })
    expect(powershell?.register).toContain(`$info.Arguments = 'worker register --hub "https://hub.example/path" --registration-key "it''s a key"'`)
    expect(powershell?.register).toContain('System.Diagnostics.ProcessStartInfo')
  })

  it('keeps literal apostrophes inside POSIX words', () => {
    const { register } = workerRegistrationCommands({
      hubUrl: 'unix:/tmp/leapmux hub.sock',
      registrationKey: 'a\'b',
      shell: 'posix',
    }) ?? {}
    expect(register).toBe(`leapmux worker register --hub 'unix:/tmp/leapmux hub.sock' --registration-key 'a'\\''b'`)
  })

  it('rejects prohibited control characters in every argument', () => {
    // The first and last C0 controls, a newline between them, and DEL.
    for (const control of [
      '\u0000',
      '\u0007',
      '\n',
      '\u001F',
      '\u007F',
    ]) {
      for (const shell of ['posix', 'powershell'] as const) {
        expect(workerRegistrationCommands({ hubUrl: `http://h${control}b`, registrationKey: 'k', shell })).toBeNull()
        expect(workerRegistrationCommands({ hubUrl: 'http://hb', registrationKey: `k${control}`, shell })).toBeNull()
      }
    }
    expect(workerRegistrationCommands({ hubUrl: 'http://hb', registrationKey: 'k', shell: 'powershell' })).not.toBeNull()
  })

  it('never joins registration and startup into one command', () => {
    for (const shell of ['posix', 'powershell'] as const) {
      const { register, start } = workerRegistrationCommands({
        hubUrl: 'https://hub.example',
        registrationKey: 'KEY',
        shell,
      }) ?? {}
      expect(register).not.toContain('worker start')
      expect(register).not.toContain('\n')
      expect(start).toBe('leapmux worker start')
    }
  })

  it('refuses empty arguments', () => {
    expect(workerRegistrationCommands({ hubUrl: '', registrationKey: 'k', shell: 'posix' })).toBeNull()
    expect(workerRegistrationCommands({ hubUrl: 'https://hub.example', registrationKey: '', shell: 'posix' })).toBeNull()
  })
})
