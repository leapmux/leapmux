import { describe, expect, it } from 'vitest'
import { isUnsafeCharacter, workerRegistrationCommands } from './workerRegistrationCommands'

describe('workerRegistrationCommands', () => {
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
    expect(powershell?.register).toBe(`leapmux worker register --hub 'https://hub.example/path' --registration-key 'it''s a key'`)
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
    expect(isUnsafeCharacter('\u0007')).toBe(true)
    expect(workerRegistrationCommands({ hubUrl: 'http://h\u0000b', registrationKey: 'k', shell: 'posix' })).toBeNull()
    expect(workerRegistrationCommands({ hubUrl: 'http://hb', registrationKey: 'k\n', shell: 'posix' })).toBeNull()
    expect(workerRegistrationCommands({ hubUrl: 'http://hb', registrationKey: 'k', shell: 'powershell' })).not.toBeNull()
  })

  it('never joins registration and startup into one command', () => {
    const { register, start } = workerRegistrationCommands({
      hubUrl: 'https://hub.example',
      registrationKey: 'KEY',
      shell: 'posix',
    }) ?? {}
    expect(register).not.toContain('start')
    expect(register).not.toContain('\n')
    expect(start).toBe('leapmux worker start')
  })

  it('refuses empty arguments', () => {
    expect(workerRegistrationCommands({ hubUrl: '', registrationKey: 'k', shell: 'posix' })).toBeNull()
    expect(workerRegistrationCommands({ hubUrl: 'https://hub.example', registrationKey: '', shell: 'posix' })).toBeNull()
  })
})
