import { describe, expect, it } from 'vitest'
import { codewhaleMcpToolResult } from './mcpToolResult'

describe('codewhaleMcpToolResult', () => {
  it('keeps the native approval receipt outside the compact MCP result', () => {
    const output = { source: 'compute the native output', text: 'first\nmiddle\nlast', firstMarker: 'first', omittedMarker: 'middle', lastMarker: 'last' }
    const result = codewhaleMcpToolResult(output, { approvedByUser: true })
    const prefix = '[approval] This tool call required approval and was approved by the user before execution.\n\n'
    const native = '{"content":[{"type":"text","text":"first\\nmiddle\\nlast"},{"type":"text","text":""}],"structuredContent":{"nextCount":0,"enabled":false,"text":""},"_meta":{"privateFixture":true}}'
    expect(result.capture.text).toBe(prefix + native)
    expect(result.copyText).toBe(prefix + native)
  })
  it('preserves native compact object order and structured zero, false and empty values', () => {
    const output = { source: 'compute the native output', text: 'first é🙂\nmiddle\nlast', firstMarker: 'first', omittedMarker: 'middle', lastMarker: 'last' }
    const result = codewhaleMcpToolResult(output)
    expect(result.capture).toEqual({ ...output, text: '{"content":[{"type":"text","text":"first é🙂\\nmiddle\\nlast"},{"type":"text","text":""}],"structuredContent":{"nextCount":0,"enabled":false,"text":""},"_meta":{"privateFixture":true}}' })
    expect(result.nativeResult.content.map(block => block.text)).toEqual([output.text, ''])
    expect(result.copyText.startsWith(`${output.text}\n\n`)).toBe(true)
    expect(JSON.parse(result.copyText.slice(output.text.length + 2))).toEqual({ structuredContent: { nextCount: 0, enabled: false, text: '' }, _meta: { privateFixture: true } })
    expect(output.text).toBe('first é🙂\nmiddle\nlast')
  })
})
