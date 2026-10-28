import { describe, expect, it } from 'vitest'
import { reasonixNativeOutput } from './nativeToolOutput'

function fixture() {
  const path = '/native/session.outputs/native-call.txt'
  const text = `[bash output kept out of context: 6001 lines, 632 KB]\nFull output: ${path}\nRead a window of it with read_file (offset/limit), or search it with grep. It is not in this conversation.\n\nOpening lines:\nnative head\n`
  const original = { sessionUpdate: 'tool_call_update', toolCallId: 'native-call', status: 'completed', content: [{ type: 'content', content: { type: 'text', text } }] }
  const provider = { sessionUpdate: original.sessionUpdate, toolCallId: original.toolCallId, status: original.status, rawOutput: { reasonix: { role: 'tool', tool_call_id: 'native-call', name: 'bash', content: text } } }
  return { original, provider, supplemental: { provider }, text, path }
}

describe('reasonixNativeOutput', () => {
  it('keeps the actual native preview and exposes the matching output file', () => {
    const { original, supplemental, text, path } = fixture()
    expect(reasonixNativeOutput(original, supplemental, 'native-call')).toEqual({ callId: 'native-call', excerpt: text, text, previewText: text, path })
  })

  it.each([
    ['call', 'belongs to another result'],
    ['status', 'belongs to another result'],
    ['native-call', 'another tool identity'],
    ['tool', 'another tool identity'],
    ['content', 'does not match its stored receipt'],
    ['path', 'no exact filesystem receipt'],
    ['relative', 'no exact filesystem receipt'],
    ['ordinary text', 'no exact filesystem receipt'],
  ])('rejects a conflicting %s receipt', (field, error) => {
    const { original, supplemental, provider } = fixture()
    if (field === 'call')
      provider.toolCallId = 'other'
    if (field === 'status')
      provider.status = 'in_progress'
    if (field === 'native-call')
      provider.rawOutput.reasonix.tool_call_id = 'other'
    if (field === 'tool')
      provider.rawOutput.reasonix.name = 'read_file'
    if (field === 'content')
      provider.rawOutput.reasonix.content = 'other content'
    if (field === 'path')
      original.content[0]!.content.text = original.content[0]!.content.text.replace('native-call.txt', 'other.txt')
    if (field === 'relative')
      original.content[0]!.content.text = original.content[0]!.content.text.replace('/native/session.outputs/', '')
    if (field === 'ordinary text')
      original.content[0]!.content.text = 'Full output: /native/session.outputs/native-call.txt'
    if (field !== 'content')
      provider.rawOutput.reasonix.content = original.content[0]!.content.text
    expect(() => reasonixNativeOutput(original, supplemental, 'native-call')).toThrow(error)
  })
})
