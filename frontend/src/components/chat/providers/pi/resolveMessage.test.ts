import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { resolveMessageForRendering } from '../registry'
import { input } from '../testUtils'
import { resolvePiMessage } from './resolveMessage'
import './plugin'

const fixturePath = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../../../testdata/pi_message_content_conformance.json')
const fixture = JSON.parse(readFileSync(fixturePath, 'utf8')) as {
  cases: Array<{ name: string, original: Record<string, unknown>, supplemental: unknown, expected: Record<string, unknown> }>
}

describe('pi native and partial result conformance', () => {
  it('loads a nonempty shared corpus', () => {
    expect(fixture.cases.length).toBeGreaterThan(0)
  })

  it.each(fixture.cases)('$name', ({ original, supplemental, expected }) => {
    const before = JSON.stringify({ original, supplemental })
    const parsed = { ...input(original), supplementalContent: supplemental }
    const resolved = resolveMessageForRendering(parsed, AgentProvider.PI)
    expect(resolved.parentObject).toEqual(expected)
    expect(resolveMessageForRendering(resolved, AgentProvider.PI)).toBe(resolved)
    expect(JSON.stringify({ original, supplemental })).toBe(before)
  })
})

describe('resolvePiMessage', () => {
  const path = '/project/.tmp/pi-mcp-0123456789abcdef.txt'
  const original = {
    type: 'tool_execution_end',
    toolCallId: 'mcp-call',
    toolName: 'mcp__sample__lookup',
    result: {
      content: [{ type: 'text', text: 'Short model preview' }, { type: 'image', data: 'image', mimeType: 'image/png' }],
      details: { server: 'sample', tool: 'lookup', fullOutputPath: path },
      structuredContent: { content: [{ type: 'text', text: 'Complete native output' }] },
    },
  }
  const supplement = (text: string) => ({ toolCallId: original.toolCallId, toolName: original.toolName, outputFile: { path, text } })

  it.each(['Complete native output', ''])('keeps native text when supplemental output is present: %j', (text) => {
    const before = JSON.stringify(original)
    const parsed = { ...input(original), supplementalContent: supplement(text) }
    const restored = resolvePiMessage(parsed)
    expect(restored).toBe(original)
    expect(resolvePiMessage({ ...parsed, parentObject: restored })).toBe(restored)
    expect(JSON.stringify(original)).toBe(before)
  })

  it('ignores output text from another call or path', () => {
    for (const extra of [
      { ...supplement('wrong'), toolCallId: 'other' },
      { ...supplement('wrong'), toolName: 'mcp__sample__other' },
      { ...supplement('wrong'), outputFile: { path: `${path}.other`, text: 'wrong' } },
      { ...supplement('wrong'), outputFile: { path, text: null } },
    ]) {
      expect(resolvePiMessage({ ...input(original), supplementalContent: extra })).toBe(original)
    }
  })

  it('keeps frames without a valid first text block or saved path unchanged', () => {
    for (const result of [
      { ...original.result, content: [] },
      { ...original.result, content: [{ type: 'image', data: 'image', mimeType: 'image/png' }] },
      { ...original.result, content: [{ type: 'text', text: null }] },
      { ...original.result, details: { server: 'sample', tool: 'lookup' } },
    ]) {
      const frame = { ...original, result }
      expect(resolvePiMessage({ ...input(frame), supplementalContent: supplement('Full output') })).toBe(frame)
    }
  })

  it('preserves native codemode text and adjacent images', () => {
    const codemodePath = '/project/.tmp/pi-codemode-0123456789abcdef.txt'
    const frame = { ...original, toolName: 'codemode', result: { content: [{ type: 'text', text: 'Script completed\nOutput:' }, { type: 'text', text: 'Clipped script output' }, { type: 'image', data: 'image', mimeType: 'image/png' }], details: { calls: [], fullOutputPath: codemodePath } } }
    expect(resolvePiMessage({ ...input(frame), supplementalContent: { toolCallId: frame.toolCallId, toolName: frame.toolName, outputFile: { path: codemodePath, text: 'Full script output' } } })).toBe(frame)
  })
})
