import { execFileSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { describe, expect, it } from 'vitest'
import { writeMcpReceiptRuntime } from './mcpReceiptRuntime'
import { readMcpServerReceipt } from './mcpServerReceipt'

describe('writeMcpReceiptRuntime', () => {
  it('stores raw JSON values through the same atomic writer', () => {
    const scratch = resolve(import.meta.dirname, '../../../..', '.tmp')
    mkdirSync(scratch, { recursive: true })
    const directory = mkdtempSync(join(scratch, 'mcp-raw-receipt-'))
    try {
      const runtime = writeMcpReceiptRuntime(directory)
      const path = join(directory, 'raw.json')
      const script = `import {writeMcpReceiptValue} from ${JSON.stringify(pathToFileURL(runtime).href)}; writeMcpReceiptValue(${JSON.stringify(path)}, [{request:{id:0},reply:{result:{count:0,enabled:false,text:''}}}]);`
      execFileSync(process.execPath, ['--input-type=module', '-e', script], { encoding: 'utf8', timeout: 30000 })
      expect(JSON.parse(readFileSync(path, 'utf8'))).toEqual([{ request: { id: 0 }, reply: { result: { count: 0, enabled: false, text: '' } } }])
      expect(readdirSync(directory).filter(file => file.includes('.writing-'))).toEqual([])
    }
    finally {
      rmSync(directory, { recursive: true, force: true })
    }
  })

  it('keeps independent native service receipts and replaces complete files atomically', () => {
    const scratch = resolve(import.meta.dirname, '../../../..', '.tmp')
    mkdirSync(scratch, { recursive: true })
    const directory = mkdtempSync(join(scratch, 'mcp-runtime-'))
    try {
      const runtime = writeMcpReceiptRuntime(directory)
      const firstPath = join(directory, 'first.json')
      const secondPath = join(directory, 'second.json')
      const script = [
        `import {createMcpServerReceipt} from ${JSON.stringify(pathToFileURL(runtime).href)};`,
        `const first=createMcpServerReceipt(${JSON.stringify(firstPath)});`,
        `const second=createMcpServerReceipt(${JSON.stringify(secondPath)});`,
        'first.initialized({roots:{listChanged:false}});',
        'first.listed(0,[{name:"echo",inputSchema:{type:"object"}}]);',
        'first.completed(1,"echo","MCP_ECHO:actual",false);',
        'first.received({jsonrpc:"2.0",id:1,method:"tools/call"});',
        'first.sent({jsonrpc:"2.0",id:1,result:{}});',
      ].join('\n')
      execFileSync(process.execPath, ['--input-type=module', '-e', script], { encoding: 'utf8', timeout: 30000 })
      expect(readMcpServerReceipt(firstPath)).toEqual({ initializeCapabilities: { roots: { listChanged: false } }, toolCatalogs: [{ id: 0, tools: [{ name: 'echo', inputSchema: { type: 'object' } }] }], elicitationRequests: [], elicitationReplies: [], toolResults: [{ id: 1, tool: 'echo', text: 'MCP_ECHO:actual', isError: false }], exchange: [{ received: { jsonrpc: '2.0', id: 1, method: 'tools/call' } }, { sent: { jsonrpc: '2.0', id: 1, result: {} } }] })
      expect(readMcpServerReceipt(secondPath)).toEqual({ initializeCapabilities: null, toolCatalogs: [], elicitationRequests: [], elicitationReplies: [], toolResults: [], exchange: [] })
      expect(readdirSync(directory).filter(path => path.includes('.writing-'))).toEqual([])
    }
    finally {
      rmSync(directory, { recursive: true, force: true })
    }
  })
})
