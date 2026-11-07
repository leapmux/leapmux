import assert from 'node:assert/strict'
import { writeFile } from 'node:fs/promises'
import path from 'node:path'
import process from 'node:process'
import { afterEach, it } from 'bun:test'
import { isAbsoluteServiceAccountFile, validateServiceAccountFile } from './assert-service-account-file.mjs'
import { scratchDirectories } from './test-support.mjs'

const scratch = scratchDirectories()
afterEach(scratch.cleanup)

it('accepts Windows drive and UNC paths without Unix rewriting', () => {
  for (const value of ['C:/private/account.yaml', String.raw`C:\private\account.yaml`, String.raw`\\server\share\account.yaml`])
    assert.equal(isAbsoluteServiceAccountFile(value, 'win32'), true, value)
  for (const value of [
    '',
    'account.yaml',
    'C:account.yaml',
    String.raw`\private\account.yaml`,
    '/private/account.yaml',
  ])
    assert.equal(isAbsoluteServiceAccountFile(value, 'win32'), false, value)
  assert.equal(isAbsoluteServiceAccountFile('/private/account.yaml', 'darwin'), true)
  assert.equal(isAbsoluteServiceAccountFile('private/account.yaml', 'linux'), false)
})

it('validates a real absolute file and refuses missing or directory inputs', async () => {
  const directory = scratch.create('native-account-path')
  const fixture = path.join(directory, 'account.yaml')
  await writeFile(fixture, 'disposable_environment: true\n', { mode: 0o600 })
  assert.equal(await validateServiceAccountFile(fixture), fixture)
  await assert.rejects(validateServiceAccountFile(directory), /regular file/)
  await assert.rejects(validateServiceAccountFile(path.join(directory, 'missing.yaml')), /ENOENT/)
  await assert.rejects(validateServiceAccountFile(path.relative(process.cwd(), fixture)), /absolute/)
})
