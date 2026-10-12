import type { ChildProcessWithoutNullStreams } from 'node:child_process'
import { Buffer } from 'node:buffer'
import { spawn } from 'node:child_process'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { stopProcess } from '../helpers/process'
import { isAlive } from '../helpers/processTree'
import { readDiracMcpSessionObservation, rewriteDiracMcpRequest, writeDiracMcpWrapper } from './mcpConfiguration'

let directory: string
const children: ChildProcessWithoutNullStreams[] = []
const server = { name: 'form_probe', command: process.execPath, args: ['private form 한글.mjs', ''], env: [{ name: 'EMPTY', value: '' }] }

beforeEach(() => {
  const scratch = resolve(process.cwd(), '../.tmp')
  mkdirSync(scratch, { recursive: true })
  directory = mkdtempSync(join(scratch, 'dirac-mcp-configuration-'))
})
afterEach(async () => {
  await Promise.all(children.splice(0).map(child => stopProcess(child)))
  rmSync(directory, { recursive: true, force: true })
})

describe('rewriteDiracMcpRequest', () => {
  it('replaces only the server list and preserves numeric request zero and unknown fields', () => {
    const frame = { jsonrpc: '2.0', id: 0, method: 'session/new', extra: false, params: { cwd: 'quoted "미" path', mcpServers: [], _meta: { unknown: 0 } } }
    const changed = rewriteDiracMcpRequest(JSON.stringify(frame), [server])
    expect(JSON.parse(changed.line)).toEqual({ ...frame, params: { ...frame.params, mcpServers: [server] } })
    expect(changed.request).toEqual(JSON.parse(changed.line))
    expect(frame.params.mcpServers).toEqual([])
  })
  it.each([
    '',
    '{',
    '[]',
    'null',
    JSON.stringify({ id: 1, method: 'initialize', params: { unknown: false } }),
    JSON.stringify({ id: 2, method: 'session/prompt', params: { prompt: [] } }),
    JSON.stringify({ method: 'session/new', params: {} }),
    JSON.stringify({ id: null, method: 'session/new', params: {} }),
    JSON.stringify({ id: 0, method: 'session/new', params: [] }),
    '{"id":1e999,"method":"session/new","params":{}}',
  ])('forwards every unrelated or malformed request unchanged: %j', (line) => {
    expect(rewriteDiracMcpRequest(line, [server])).toEqual({ line })
  })
})

describe('writeDiracMcpWrapper', () => {
  it('forwards real native bytes and argv while configuring the actual zero-ID session', async () => {
    const executable = join(directory, 'actual-native.cjs')
    writeFileSync(executable, `
const {createInterface}=require('node:readline');
process.stderr.write('NATIVE_STDERR_미\\n');
for(const argument of process.argv.slice(2))process.stderr.write('ARGV:'+argument+'\\n');
createInterface({input:process.stdin}).on('line',line=>{
  let frame;try{frame=JSON.parse(line)}catch{process.stdout.write('UNCHANGED_NATIVE_PARSE_ERROR\\n');return}
  const result=frame.method==='session/new'?{sessionId:'actual-native-session',received:frame}:{received:frame};
  process.stdout.write(JSON.stringify({jsonrpc:'2.0',id:frame.id,result})+'\\n');
});
`)
    const wrapper = writeDiracMcpWrapper({ directory: join(directory, 'wrapper'), executable: process.execPath, args: [executable], nodeExecutable: process.execPath, servers: [server] })
    const child = spawn(process.execPath, [wrapper.scriptPath, '--acp', 'quoted 한글 argument'], { cwd: directory, stdio: ['pipe', 'pipe', 'pipe'] })
    children.push(child)
    let stdout = ''
    let stderr = ''
    child.stdout.setEncoding('utf8').on('data', bytes => stdout += bytes)
    child.stderr.setEncoding('utf8').on('data', bytes => stderr += bytes)
    const closed = new Promise<void>((resolve, reject) => {
      child.once('error', reject)
      child.once('close', code => code === 0 ? resolve() : reject(new Error(`The native wrapper test exited with code ${code}.`)))
    })
    const original = { jsonrpc: '2.0', id: 'unrelated', method: 'initialize', params: { value: 0 } }
    const session = { jsonrpc: '2.0', id: 0, method: 'session/new', params: { cwd: directory, unknown: '미', mcpServers: [] } }
    const bytes = Buffer.from(`${JSON.stringify(original)}\ninvalid-native-json\n${JSON.stringify(session)}\n`)
    const split = bytes.indexOf(Buffer.from('미')) + 1
    await new Promise<void>((resolve, reject) => child.stdin.write(bytes.subarray(0, split), error => error ? reject(error) : resolve()))
    child.stdin.end(bytes.subarray(split))
    await closed
    const lines = stdout.trimEnd().split('\n')
    expect(JSON.parse(lines[0] ?? '').result.received).toEqual(original)
    expect(lines[1]).toBe('UNCHANGED_NATIVE_PARSE_ERROR')
    expect(JSON.parse(lines[2] ?? '').result.received).toEqual({ ...session, params: { ...session.params, mcpServers: [server] } })
    expect(stderr).toBe('NATIVE_STDERR_미\nARGV:--acp\nARGV:quoted 한글 argument\n')
    const observed = readDiracMcpSessionObservation(wrapper.receiptLog)
    expect(observed.request.id).toBe(0)
    expect(observed.reply.id).toBe(0)
    expect(observed.sessionId).toBe('actual-native-session')
  })
  it('rejects duplicate servers and relative executable paths before creating a wrapper', () => {
    const options = { directory: join(directory, 'wrapper'), executable: process.execPath, nodeExecutable: process.execPath, servers: [server] }
    expect(() => writeDiracMcpWrapper({ ...options, servers: [server, server] })).toThrow('unique name')
    expect(() => writeDiracMcpWrapper({ ...options, executable: 'dirac' })).toThrow('absolute native paths')
    expect(() => writeDiracMcpWrapper({ ...options, servers: [{ ...server, command: 'node' }] })).toThrow('absolute stdio command')
  })

  it('preserves the native exit status and argv outside ACP mode', async () => {
    const executable = join(directory, 'native-version.cjs')
    writeFileSync(executable, 'process.stdout.write(JSON.stringify(process.argv.slice(2)));process.exitCode=7')
    const wrapper = writeDiracMcpWrapper({ directory: join(directory, 'wrapper'), executable: process.execPath, args: [executable], nodeExecutable: process.execPath, servers: [server] })
    const child = spawn(process.execPath, [wrapper.scriptPath, '--version', 'native value'], { cwd: directory, stdio: ['pipe', 'pipe', 'pipe'] })
    children.push(child)
    let output = ''
    child.stdout.setEncoding('utf8').on('data', value => output += value)
    const code = await new Promise<number | null>((resolve, reject) => {
      child.once('close', resolve)
      child.once('error', reject)
    })
    expect(code).toBe(7)
    expect(JSON.parse(output)).toEqual(['--version', 'native value'])
  })

  it.each(['null', '{}', '{"name":"form","command":0}', '{"name":"form","command":"/node","args":[],"env":[null]}'])('rejects malformed native server fields before it writes a wrapper: %j', (bytes) => {
    expect(() => writeDiracMcpWrapper({ directory: join(directory, 'wrapper'), executable: process.execPath, nodeExecutable: process.execPath, servers: [JSON.parse(bytes)] })).toThrow('absolute stdio command')
  })

  it('retains the spawn failure exit status when the native executable is absent', async () => {
    const wrapper = writeDiracMcpWrapper({ directory: join(directory, 'wrapper'), executable: join(directory, 'missing-native'), nodeExecutable: process.execPath, servers: [server] })
    const child = spawn(process.execPath, [wrapper.scriptPath, '--acp'], { cwd: directory, stdio: ['pipe', 'pipe', 'pipe'] })
    children.push(child)
    let stderr = ''
    child.stderr.setEncoding('utf8').on('data', value => stderr += value)
    const code = await new Promise<number | null>((resolve, reject) => {
      child.once('close', resolve)
      child.once('error', reject)
    })
    expect(code).toBe(127)
    expect(stderr).toContain('The native Dirac MCP wrapper failed:')
  })

  it('forwards shutdown to the actual child and preserves its signal', async () => {
    const executable = join(directory, 'native-held.cjs')
    writeFileSync(executable, 'process.stdin.resume();process.stdout.write(JSON.stringify({pid:process.pid})+"\\n")')
    const wrapper = writeDiracMcpWrapper({ directory: join(directory, 'wrapper'), executable: process.execPath, args: [executable], nodeExecutable: process.execPath, servers: [server] })
    const child = spawn(process.execPath, [wrapper.scriptPath, '--version'], { cwd: directory, stdio: ['pipe', 'pipe', 'pipe'] })
    children.push(child)
    let output = ''
    const ready = new Promise<number>((resolve, reject) => {
      child.once('error', reject)
      child.stdout.on('data', (bytes) => {
        output += bytes.toString('utf8')
        const end = output.indexOf('\n')
        if (end < 0)
          return
        try {
          const pid: unknown = JSON.parse(output.slice(0, end)).pid
          if (typeof pid !== 'number' || !Number.isSafeInteger(pid) || pid <= 0)
            throw new Error('The held native child returned no valid process ID.')
          resolve(pid)
        }
        catch (error) {
          reject(error)
        }
      })
    })
    const pid = await ready
    const closed = new Promise<void>((resolve, reject) => {
      child.once('close', () => resolve())
      child.once('error', reject)
    })
    expect(child.kill('SIGTERM')).toBe(true)
    await closed
    expect(child.signalCode).toBe('SIGTERM')
    expect(isAlive(pid)).toBe(false)
  })
})

describe('readDiracMcpSessionObservation', () => {
  const request = { direction: 'request', frame: { jsonrpc: '2.0', id: 0, method: 'session/new', params: { cwd: '/private', mcpServers: [server] } } }
  const reply = { direction: 'reply', frame: { jsonrpc: '2.0', id: 0, result: { sessionId: 'native' } } }
  it.each([
    { label: 'missing request', records: [], reason: 'one actual session creation request' },
    { label: 'missing reply', records: [request], reason: 'unique successful session reply' },
    { label: 'different ID type', records: [request, { ...reply, frame: { ...reply.frame, id: '0' } }], reason: 'unique successful session reply' },
    { label: 'native error', records: [request, { ...reply, frame: { id: 0, error: { code: -1 } } }], reason: 'unique successful session reply' },
    { label: 'empty session ID', records: [request, { ...reply, frame: { ...reply.frame, result: { sessionId: '' } } }], reason: 'unique successful session reply' },
    { label: 'duplicate request', records: [request, request], reason: 'one actual session creation request' },
    { label: 'duplicate reply', records: [request, reply, reply], reason: 'unique successful session reply' },
    { label: 'invalid request', records: [{ direction: 'request', frame: { id: 0 } }, reply], reason: 'invalid session creation request' },
  ])('rejects the $label native session record', ({ records, reason }) => {
    const path = join(directory, 'receipt.jsonl')
    writeFileSync(path, records.map(record => JSON.stringify(record)).join('\n'))
    expect(() => readDiracMcpSessionObservation(path)).toThrow(reason)
  })

  it('rejects corrupt native record bytes', () => {
    const path = join(directory, 'receipt.jsonl')
    writeFileSync(path, '{\n')
    expect(() => readDiracMcpSessionObservation(path)).toThrow(SyntaxError)
  })
})
