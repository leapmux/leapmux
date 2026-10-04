import type { McpResultContent } from './mcpResultServer'
import { Buffer } from 'node:buffer'
import { execFileSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { writeMcpResultServer } from './mcpResultServer'
import { writeToolImage } from './toolImages'

function execute(method: string, params: unknown, inspectContent?: (imagePath: string) => readonly McpResultContent[], inspectNullable?: null) {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  const directory = mkdtempSync(join(scratch, 'mcp-result-unit-'))
  try {
    const image = writeToolImage(directory, 'resource')
    const receiptLog = join(directory, 'receipt.json')
    const imagePath = join(directory, image)
    const script = writeMcpResultServer(directory, { receiptLog, imagePath, ...(inspectContent ? { inspectContent: inspectContent(imagePath) } : {}), ...(inspectNullable === undefined ? {} : { inspectNullable }) })
    const request = { jsonrpc: '2.0', id: 0, method, params }
    const output = execFileSync(process.execPath, [script], { input: `${JSON.stringify(request)}\n`, encoding: 'utf8', timeout: 30000 })
    return { reply: JSON.parse(output), receipt: JSON.parse(readFileSync(receiptLog, 'utf8')) }
  }
  finally {
    rmSync(directory, { recursive: true, force: true })
  }
}

describe('writeMcpResultServer', () => {
  it('returns configured text and image occurrences in order without changing structured results', () => {
    const args = { count: 0, enabled: false, text: '' }
    const { reply, receipt } = execute('tools/call', { name: 'inspect', arguments: args }, path => [{ type: 'text', text: 'First native text.' }, { type: 'image', path }, { type: 'text', text: 'Middle native text.' }, { type: 'image', path }, { type: 'text', text: '' }])
    expect(reply.result.content.map((block: { type: string }) => block.type)).toEqual(['text', 'image', 'text', 'image', 'text'])
    expect(reply.result.content[0]).toEqual({ type: 'text', text: 'First native text.' })
    expect(reply.result.content[4]).toEqual({ type: 'text', text: '' })
    expect(reply.result.content[1]).toEqual(reply.result.content[3])
    expect(reply.result.content[1].mimeType).toBe('image/png')
    expect(Buffer.from(reply.result.content[1].data, 'base64').subarray(1, 4).toString()).toBe('PNG')
    expect(reply.result.structuredContent).toEqual({ nextCount: 1, enabled: false, text: '' })
    expect(reply.result._meta).toEqual({ privateFixture: true })
    expect(receipt[1].reply).toEqual(reply)
  })

  it('allows empty configured content while preserving the computed structured result', () => {
    const args = { count: -1, enabled: false, text: '' }
    const { reply } = execute('tools/call', { name: 'inspect', arguments: args }, () => [])
    expect(reply.result.content).toEqual([])
    expect(reply.result.structuredContent).toEqual({ nextCount: 0, enabled: false, text: '' })
  })

  it('refuses a missing configured image before the subprocess starts', () => {
    expect(() => execute('tools/call', { name: 'inspect', arguments: { count: 0, enabled: false, text: '' } }, path => [{ type: 'image', path: `${path}.missing` }])).toThrow('existing image paths')
  })
  it.each([{ count: 0, enabled: false, text: '' }, { count: -1, enabled: true, text: '출력' }])('computes structured values from exact native arguments: %j', (args) => {
    const { reply, receipt } = execute('tools/call', { name: 'inspect', arguments: args })
    expect(reply.id).toBe(0)
    expect(reply.result.structuredContent).toEqual({ nextCount: args.count + 1, enabled: args.enabled, text: args.text })
    expect(receipt[0].request.params.arguments).toEqual(args)
    expect(receipt[1].reply).toEqual(reply)
  })

  it.each([null, {}, { count: 0, enabled: false, text: '', extra: true }, { count: 0.5, enabled: false, text: '' }])('rejects invalid argument boundaries: %j', (args) => {
    expect(execute('tools/call', { name: 'inspect', arguments: args }).reply.error.code).toBe(-32602)
  })

  it('returns a native failed result without a protocol error', () => {
    const reply = execute('tools/call', { name: 'fail', arguments: {} }).reply
    expect(reply.result.isError).toBe(true)
    expect(reply.result.structuredContent).toEqual({ failed: true, count: 0 })
    expect(reply.error).toBeUndefined()
  })

  it('keeps an explicitly configured null through the native protocol and receipt', () => {
    const args = { count: 0, enabled: false, text: '' }
    const { reply, receipt } = execute('tools/call', { name: 'inspect', arguments: args }, undefined, null)
    expect(reply.id).toBe(0)
    expect(reply.result.structuredContent).toEqual({ nextCount: 1, enabled: false, text: '', nullable: null })
    expect(Object.hasOwn(reply.result.structuredContent, 'nullable')).toBe(true)
    expect(reply.result._meta).toEqual({ privateFixture: true })
    expect(receipt[0].request.params.arguments).toEqual(args)
    expect(receipt[1].reply).toEqual(reply)
  })

  it('omits the nullable field when no caller selects it', () => {
    const { reply } = execute('tools/call', { name: 'inspect', arguments: { count: 0, enabled: false, text: '' } })
    expect(Object.hasOwn(reply.result.structuredContent, 'nullable')).toBe(false)
  })

  it.each([false, 0, '', [], {}])('refuses a non-null nullable fixture value %j', (inspectNullable) => {
    expect(() => Reflect.apply(writeMcpResultServer, undefined, ['', { receiptLog: '', inspectNullable }])).toThrow('explicit null value')
  })

  it('supplies lists, templates, text, and image resources', () => {
    expect(execute('resources/list', {}).reply.result.resources).toHaveLength(2)
    expect(execute('resources/templates/list', {}).reply.result.resourceTemplates[0].uriTemplate).toBe('probe://template/{id}')
    expect(execute('resources/read', { uri: 'probe://text' }).reply.result.contents[0].text).toBe('NATIVE_MCP_RESOURCE_TEXT')
    const image = execute('resources/read', { uri: 'probe://image' }).reply.result.contents[0]
    expect(image.mimeType).toBe('image/png')
    expect(Buffer.from(image.blob, 'base64').subarray(1, 4).toString()).toBe('PNG')
  })

  it('rejects an unknown resource and an unsupported method', () => {
    expect(execute('resources/read', { uri: '' }).reply.error.code).toBe(-32602)
    expect(execute('missing/method', {}).reply.error.code).toBe(-32601)
  })
})
