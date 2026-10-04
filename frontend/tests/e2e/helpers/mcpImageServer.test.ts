import { execFileSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { writeMcpImageServer } from './mcpImageServer'
import { writeToolImage } from './toolImages'

function execute(name: string, args: unknown) {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  const directory = mkdtempSync(join(scratch, 'mcp-image-unit-'))
  try {
    const image = writeToolImage(directory, 'actual')
    const server = writeMcpImageServer(directory, image)
    const request = { jsonrpc: '2.0', id: 0, method: 'tools/call', params: { name, arguments: args } }
    return JSON.parse(execFileSync(process.execPath, [server.script], { input: `${JSON.stringify(request)}\n`, encoding: 'utf8', timeout: 30000 }))
  }
  finally {
    rmSync(directory, { recursive: true, force: true })
  }
}

describe('writeMcpImageServer', () => {
  it('returns the actual image for its exact native tool', () => {
    const reply = execute('show', {})
    expect(reply.id).toBe(0)
    expect(reply.result.content[1]).toMatchObject({ type: 'image', mimeType: 'image/png' })
  })

  it.each([{ name: 'wrong', args: {} }, { name: 'show', args: null }, { name: 'show', args: { extra: true } }])('rejects a wrong call before returning an image: %j', ({ name, args }) => {
    const reply = execute(name, args)
    expect(reply.error?.code).toBe(-32602)
    expect(reply.result).toBeUndefined()
  })
})
