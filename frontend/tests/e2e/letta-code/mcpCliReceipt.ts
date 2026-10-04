import { randomUUID } from 'node:crypto'
import { lstatSync, mkdtempSync, readFileSync, realpathSync, writeFileSync } from 'node:fs'
import { constants } from 'node:os'
import { isAbsolute, join } from 'node:path'
import { isObject } from '../../../src/lib/jsonPick'

export interface LettaMcpCliReceipt {
  receiptId: string
  callId: string
  executable: string
  args: string[]
  stdout: string
  stderr: string
  exitCode: number | null
  signal: string | null
  spawnError: string | null
}

export type LettaMcpCliIdentity = Pick<LettaMcpCliReceipt, 'receiptId' | 'callId' | 'executable' | 'args'>

export interface LettaMcpCliCapture {
  scriptPath: string
  receiptPath: string
  receiptId: string
}

/** Create a native CLI capture that publishes one complete receipt without replacement. */
export function writeLettaMcpCliCapture(directory: string): LettaMcpCliCapture {
  if (!isAbsolute(directory) || !lstatSync(directory).isDirectory())
    throw new Error('The Letta MCP capture requires an absolute private directory.')
  const privateDirectory = mkdtempSync(join(realpathSync(directory), 'letta-mcp-cli-'))
  const receiptPath = join(privateDirectory, 'receipt.json')
  const scriptPath = join(privateDirectory, 'capture.cjs')
  const receiptId = randomUUID()
  const source = `
const fs = require('node:fs')
const { spawn } = require('node:child_process')
const [receiptId, callId, executable, ...args] = process.argv.slice(2)
const receiptPath = ${JSON.stringify(receiptPath)}
const partialPath = receiptPath + '.partial'
if (receiptId !== ${JSON.stringify(receiptId)} || !callId || !executable) {
  process.stderr.write('The Letta MCP capture received an invalid identity.\\n')
  process.exitCode = 1
} else {
  const stdout = []
  const stderr = []
  let spawnError = null
  const child = spawn(executable, args, { env: process.env, stdio: ['ignore', 'pipe', 'pipe'], shell: false })
  child.stdout.on('data', chunk => stdout.push(chunk))
  child.stderr.on('data', chunk => stderr.push(chunk))
  child.once('error', error => { spawnError = error.message })
  const forwardInterrupt = () => child.kill('SIGINT')
  const forwardStop = () => child.kill('SIGTERM')
  process.on('SIGINT', forwardInterrupt)
  process.on('SIGTERM', forwardStop)
  child.once('close', (exitCode, signal) => {
    process.removeListener('SIGINT', forwardInterrupt)
    process.removeListener('SIGTERM', forwardStop)
    const receipt = {
      receiptId, callId, executable, args,
      stdout: Buffer.concat(stdout).toString('utf8'),
      stderr: Buffer.concat(stderr).toString('utf8'),
      exitCode: spawnError === null ? exitCode : 127,
      signal,
      spawnError,
    }
    let ownsPartial = false
    try {
      fs.writeFileSync(partialPath, JSON.stringify(receipt) + '\\n', { flag: 'wx' })
      ownsPartial = true
      fs.linkSync(partialPath, receiptPath)
      fs.unlinkSync(partialPath)
      ownsPartial = false
      fs.writeSync(1, receipt.stdout)
      fs.writeSync(2, receipt.stderr)
      if (spawnError !== null) fs.writeSync(2, 'The Letta MCP CLI failed to start: ' + spawnError + '\\n')
      process.exitCode = spawnError === null ? (exitCode ?? 1) : 127
      fs.writeSync(1, '\\nLETTAMCPCLI:' + receiptId + ':' + callId + '\\n')
    } catch (error) {
      process.stderr.write('The Letta MCP receipt could not be published: ' + error.message + '\\n')
      process.exitCode = 1
    } finally {
      if (ownsPartial) fs.unlinkSync(partialPath)
    }
    if (signal !== null) process.kill(process.pid, signal)
  })
}
`
  writeFileSync(scriptPath, source, { flag: 'wx' })
  return { scriptPath, receiptPath, receiptId }
}

/** Read the exclusive native receipt and require its marker in the exact call-ID result. */
export function readLettaMcpCliReceipt(path: string, nativeResult: string, expected: LettaMcpCliIdentity): LettaMcpCliReceipt {
  const marker = `LETTAMCPCLI:${expected.receiptId}:${expected.callId}`
  if (nativeResult.split(/\r?\n/).filter(line => line === marker).length !== 1)
    throw new Error('The exact native Letta result must contain one complete CLI receipt marker.')
  if (!isAbsolute(path) || !lstatSync(path).isFile())
    throw new Error('The Letta MCP CLI receipt must be an actual private file.')
  const value: unknown = JSON.parse(readFileSync(path, 'utf8'))
  const fields = ['receiptId', 'callId', 'executable', 'args', 'stdout', 'stderr', 'exitCode', 'signal', 'spawnError']
  if (!isObject(value) || Object.keys(value).length !== fields.length || !fields.every(field => Object.hasOwn(value, field))
    || typeof value.receiptId !== 'string' || typeof value.callId !== 'string' || typeof value.executable !== 'string'
    || !Array.isArray(value.args) || !value.args.every((arg: unknown) => typeof arg === 'string')
    || typeof value.stdout !== 'string' || typeof value.stderr !== 'string'
    || (value.exitCode !== null && (typeof value.exitCode !== 'number' || !Number.isSafeInteger(value.exitCode) || value.exitCode < 0))
    || (value.signal !== null && (typeof value.signal !== 'string' || !Object.hasOwn(constants.signals, value.signal)))
    || (value.spawnError !== null && (typeof value.spawnError !== 'string' || value.spawnError === ''))
    || (value.exitCode === null && value.signal === null && value.spawnError === null)) {
    throw new Error('The Letta MCP CLI receipt is incomplete or malformed.')
  }
  if (!expected.receiptId || !expected.callId || !isAbsolute(expected.executable)
    || value.receiptId !== expected.receiptId || value.callId !== expected.callId || value.executable !== expected.executable
    || JSON.stringify(value.args) !== JSON.stringify(expected.args)) {
    throw new Error('The Letta MCP CLI receipt does not match the expected native invocation.')
  }
  return {
    receiptId: value.receiptId,
    callId: value.callId,
    executable: value.executable,
    args: value.args,
    stdout: value.stdout,
    stderr: value.stderr,
    exitCode: value.exitCode,
    signal: value.signal,
    spawnError: value.spawnError,
  }
}
