import { describe, expect, it } from 'vitest'
import { reasonixNativeOutput } from './nativeToolOutput'

function fixture() {
  const text = `native head\n${'x'.repeat(9000)}\ncomputed middle77\nnative tail`
  const prefix = text.slice(0, 8000)
  const excerpt = `${prefix}\n…(${text.length - prefix.length} more chars truncated)`
  const original = { sessionUpdate: 'tool_call_update', toolCallId: 'native-call', status: 'completed', content: [{ type: 'content', content: { type: 'text', text: excerpt } }] }
  const provider = { sessionUpdate: original.sessionUpdate, toolCallId: original.toolCallId, status: original.status, rawOutput: { reasonix: { role: 'tool', tool_call_id: 'native-call', name: 'bash', content: text, raw_content: text } } }
  return { original, provider, supplemental: { provider }, text }
}

describe('reasonixNativeOutput', () => {
  it('keeps complete native content outside the actual excerpt', () => {
    const { original, supplemental, text } = fixture()
    const result = reasonixNativeOutput(original, supplemental, 'native-call')
    expect(result.text).toBe(text)
    expect(result.excerpt).not.toContain('middle77')
  })
  it('uses the complete raw record when its model record is clipped', () => {
    const { original, supplemental, provider, text } = fixture()
    provider.rawOutput.reasonix.content = original.content[0]?.content.text ?? ''
    expect(reasonixNativeOutput(original, supplemental, 'native-call').text).toBe(text)
  })
  it.each(['call', 'status', 'native-call', 'tool', 'omitted', 'content'])('rejects a conflicting %s result', (field) => {
    const { original, supplemental, provider } = fixture()
    if (field === 'call')
      provider.toolCallId = 'other'
    if (field === 'status')
      provider.status = 'in_progress'
    if (field === 'native-call')
      provider.rawOutput.reasonix.tool_call_id = 'other'
    if (field === 'tool')
      provider.rawOutput.reasonix.name = 'read_file'
    if (field === 'omitted')
      original.content[0]!.content.text = 'native head\n\n…(1 more chars truncated)'
    if (field === 'content') {
      provider.rawOutput.reasonix.content = 'other content'
      provider.rawOutput.reasonix.raw_content = 'other content'
    }
    expect(() => reasonixNativeOutput(original, supplemental, 'native-call')).toThrow()
  })
  it('does not claim a native output for complete inline output', () => {
    const { original, supplemental, text } = fixture()
    original.content[0]!.content.text = text
    expect(() => reasonixNativeOutput(original, supplemental, 'native-call')).toThrow('no omitted native output content')
  })
})
