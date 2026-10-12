import { describe, expect, it } from 'vitest'
import { geminiResultContent, geminiResultImages, geminiResultText, geminiShellResult, geminiUntrustedText } from './results'

function shellRecord(output: string, report: string, display: unknown = output): Record<string, unknown> {
  return { status: 'success', resultDisplay: display, result: [{ functionResponse: { response: { output: `<untrusted_context>\n${report}\n</untrusted_context>` } } }] }
}

describe('geminiShellResult', () => {
  it('reads the native exit code from a corroborated shell report', () => {
    expect(geminiShellResult(shellRecord('computed value', 'Output: computed value\nExit Code: 7\nProcess Group PGID: 123'))).toEqual({ output: 'computed value', exitCode: 7 })
  })

  it('preserves successful stdout that resembles native failure metadata', () => {
    const output = 'data\nExit Code: 7'
    expect(geminiShellResult(shellRecord(output, `Output: ${output}\nProcess Group PGID: 123`))).toEqual({ output })
    const ansi = [[{ text: 'data' }], [{ text: 'Exit Code: 7' }, { text: '     ' }]]
    expect(geminiShellResult(shellRecord(output, `Output: ${output}\nProcess Group PGID: 123`, ansi))).toEqual({ output })
  })

  it('reads native ANSI display cells without adding their screen padding to copied output', () => {
    const display = [[{ text: 'native value' }, { text: '        ' }]]
    expect(geminiShellResult(shellRecord('native value', 'Output: native value\nExit Code: 3\nProcess Group PGID: 123', display))).toEqual({ output: 'native value', exitCode: 3 })
  })

  it('preserves zero output and distinguishes an empty failed command', () => {
    expect(geminiShellResult(shellRecord('', 'Output: (empty)\nProcess Group PGID: 123'))).toEqual({ output: '' })
    expect(geminiShellResult(shellRecord('', 'Output: (empty)\nExit Code: 7\nProcess Group PGID: 123', 'Command exited with code: 7'))).toEqual({ output: '', exitCode: 7 })
  })

  it('retains unknown native report fields without inventing an exit code', () => {
    const result = geminiShellResult(shellRecord('native value', 'Output: native value\nUnexpected field: detail\nProcess Group PGID: 123'))
    expect(result).toEqual({ output: 'native value\nUnexpected field: detail' })
  })

  it('retains a large multiline result and its final bytes', () => {
    const output = `${'native output Ω\n'.repeat(50000)}COMPLETE_NATIVE_OUTPUT\n`
    expect(geminiShellResult(shellRecord(output, `Output: ${output}\nProcess Group PGID: 123`))).toEqual({ output })
  })

  it('preserves explicit native tool failures and malformed result displays', () => {
    expect(geminiShellResult({ status: 'error', resultDisplay: 'Native execution failed.' })).toEqual({ output: 'Native execution failed.', failed: true })
    expect(geminiShellResult(shellRecord('value', 'Output: value\nExit Code: 7\nProcess Group PGID: 123', [[{ text: 0 }]]))).toEqual({ output: 'Output: value\nExit Code: 7\nProcess Group PGID: 123' })
  })
})

describe('geminiResultImages', () => {
  it('reads native sibling images and nested multimodal function images', () => {
    const image = { inlineData: { mimeType: 'image/png', data: 'native-image-bytes' } }
    const record = { result: [{ functionResponse: { response: { output: 'native result' }, parts: [image] } }, image] }
    expect(geminiResultImages(record, '/work/image.png')).toEqual([
      { mimeType: 'image/png', data: 'native-image-bytes', filePath: '/work/image.png' },
      { mimeType: 'image/png', data: 'native-image-bytes', filePath: '/work/image.png' },
    ])
    expect(geminiResultContent(record)).toEqual([
      { type: 'text', text: 'native result' },
      { type: 'image', source: { mimeType: 'image/png', data: 'native-image-bytes' } },
      { type: 'image', source: { mimeType: 'image/png', data: 'native-image-bytes' } },
    ])
  })

  it('rejects absent and non-image content without inventing bytes', () => {
    expect(geminiResultImages({})).toEqual([])
    expect(geminiResultImages({ result: [null, { inlineData: { mimeType: 'application/pdf', data: 'pdf' } }, { inlineData: { mimeType: 'image/png' } }] })).toEqual([])
  })
})

describe('geminiUntrustedText', () => {
  it('removes the exact outer wrapper and preserves nested user text', () => {
    const content = '<untrusted_context>\nuser data\n</untrusted_context>'
    expect(geminiUntrustedText(`<untrusted_context>\n${content}\n</untrusted_context>`)).toBe(content)
    expect(geminiUntrustedText(`prefix ${content}`)).toBe(`prefix ${content}`)
    expect(geminiUntrustedText('')).toBe('')
  })
})

describe('geminiResultText', () => {
  it('preserves native zero, false, and empty values inside text', () => {
    const output = 'NATIVE_MCP_INSPECT:{"count":0,"enabled":false,"text":""}'
    expect(geminiResultText({ result: [{ functionResponse: { response: { output } } }] })).toBe(output)
  })
})
