import { spawn } from 'node:child_process'
import { once } from 'node:events'
import { mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { createInterface } from 'node:readline'
import { afterEach, describe, expect, it } from 'vitest'
import { isObject } from '../../../src/lib/jsonPick'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { readMcpServerReceipt } from '../helpers/mcpServerReceipt'
import { createMcpCloseControl } from './mcpCloseControl'

const directories: string[] = []
function directory() {
  const root = join(import.meta.dirname, '../../../../.tmp/command-code-provider/mcp-close-tests')
  mkdirSync(root, { recursive: true })
  const path = mkdtempSync(join(root, 'native-close-'))
  directories.push(path)
  return path
}

afterEach(() => {
  for (const path of directories.splice(0))
    rmSync(path, { recursive: true, force: true })
})

describe('createMcpCloseControl', () => {
  it('closes the unchanged server after its real input request without inventing a reply', async () => {
    const path = directory()
    const receipt = join(path, 'receipt.json')
    const server = writeMcpFormServer(path, 'server.mjs', { receiptLog: receipt })
    const control = createMcpCloseControl(path, server)
    const child = spawn(process.execPath, [control.script], { stdio: ['pipe', 'pipe', 'pipe'], signal: AbortSignal.timeout(30000) })
    const ended = once(child, 'close')
    let stderr = ''
    child.stderr.setEncoding('utf8').on('data', text => stderr += text)
    const lines = createInterface({ input: child.stdout })
    const received: unknown[] = []
    const request = new Promise<void>((resolve, reject) => {
      child.once('error', reject)
      lines.on('line', (line) => {
        try {
          const value: unknown = JSON.parse(line)
          received.push(value)
          if (isObject(value) && value.method === 'elicitation/create')
            resolve()
        }
        catch (error) { reject(error) }
      })
      child.once('exit', () => {
        if (!received.some(value => isObject(value) && value.method === 'elicitation/create'))
          reject(new Error('The MCP server closed before its input request.'))
      })
    })
    try {
      child.stdin.write(`${JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'initialize', params: { capabilities: { roots: {} } } })}\n`)
      child.stdin.write(`${JSON.stringify({ jsonrpc: '2.0', id: 2, method: 'tools/call', params: { name: 'ask', arguments: {} } })}\n`)
      await request
      const pending = readMcpServerReceipt(receipt)
      expect(pending.elicitationRequests).toHaveLength(1)
      expect(pending.elicitationReplies).toEqual([])
      control.close()
      expect(await ended).toEqual([0, null])
      expect(stderr).toBe('')
      expect(readMcpServerReceipt(receipt).elicitationReplies).toEqual([])
      expect(received.some(value => isObject(value) && value.id === 2 && Object.hasOwn(value, 'result'))).toBe(false)
    }
    finally {
      child.stdin.end()
      if (child.exitCode === null && child.signalCode === null)
        child.kill('SIGTERM')
      await ended
      lines.close()
    }
  })

  it('refuses relative directories and absent server programs', () => {
    const path = directory()
    const server = writeMcpFormServer(path, 'server.mjs')
    expect(() => createMcpCloseControl('relative', server)).toThrow('absolute')
    expect(() => createMcpCloseControl(path, 'relative')).toThrow('absolute')
    expect(() => createMcpCloseControl(path, join(path, 'absent.mjs'))).toThrow('existing')
  })
})
