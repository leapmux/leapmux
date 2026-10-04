import type { LettaMcpCliReceipt } from './mcpCliReceipt'
import { describe, expect, it } from 'vitest'
import { parseLettaMcpCatalog } from './mcpCatalog'

const capturedTools = [{
  name: 'mcp__echo_probe__echo',
  description: 'Return the supplied value.',
  inputSchema: { type: 'object', properties: { value: { type: 'string' } }, required: ['value'] },
}]
const capturedWarning = '(node:25228) Warning: The \'NO_COLOR\' env is ignored due to the \'FORCE_COLOR\' env being set.\n'
  + '(Use `node --trace-warnings ...` to show where the warning was created)\n'

function capturedReceipt(overrides: Partial<LettaMcpCliReceipt> = {}): LettaMcpCliReceipt {
  return {
    receiptId: 'catalog-captured-format',
    callId: 'letta-native-echo_probe-catalog',
    executable: '/private/native/letta',
    args: ['mcp', 'tools', 'echo_probe', '--full', '--agent', 'agent-native'],
    stdout: `${JSON.stringify(capturedTools, null, 2)}\n`,
    stderr: capturedWarning,
    exitCode: 0,
    signal: null,
    spawnError: null,
    ...overrides,
  }
}

describe('parseLettaMcpCatalog', () => {
  it('rejects the obsolete prefix in native CLI stdout', () => {
    const tools = [{ name: 'mcp__echo_probe__echo', inputSchema: { type: 'object', required: ['value'], properties: { value: { type: 'string' } } } }]
    expect(() => parseLettaMcpCatalog(capturedReceipt({ stdout: `Exit code: 0\n${JSON.stringify(tools, null, 2)}\n` }))).toThrow()
  })
  it('reads the captured successful native stdout and preserves its separate stderr warning', () => {
    const receipt = capturedReceipt()
    expect(parseLettaMcpCatalog(receipt)).toEqual(capturedTools)
    expect(receipt.stderr).toBe(capturedWarning)
  })
  it('reads a large complete native catalog without reading stderr as a catalog', () => {
    const tools = Array.from({ length: 10_000 }, (_, index) => ({ ...capturedTools[0], name: `mcp__catalog__tool_${index}` }))
    expect(parseLettaMcpCatalog(capturedReceipt({ stdout: JSON.stringify(tools), stderr: '[{"name":"wrong"}]' }))).toEqual(tools)
  })
  it.each([
    { stdout: '' },
    { stdout: '[]' },
    { stdout: 'null' },
    { stdout: '[{"name":"echo"}]' },
    { stdout: '[{"name":"","inputSchema":{}}]' },
    { stdout: '[{"name":"echo","inputSchema":null}]' },
    { stdout: '[{"name":"echo","inputSchema":[]}]' },
    { stdout: '[' },
    { stdout: `${JSON.stringify(capturedTools)}\n${capturedWarning}` },
    { stdout: `${JSON.stringify(capturedTools)}\n${JSON.stringify(capturedTools)}` },
    { stdout: '', stderr: JSON.stringify(capturedTools) },
    { exitCode: null },
    { exitCode: -1 },
    { exitCode: 2 },
    { exitCode: 255 },
    { signal: 'SIGTERM', exitCode: null },
    { signal: 'SIGTERM', exitCode: 0 },
    { spawnError: 'spawn /private/native/letta ENOENT', exitCode: 0 },
  ] satisfies Partial<LettaMcpCliReceipt>[])('rejects an unsuccessful or incomplete native stdout receipt: %j', (overrides) => {
    expect(() => parseLettaMcpCatalog(capturedReceipt(overrides))).toThrow()
  })
  it.each(['', '[]', 'Exit code: 2\n[]', 'Exit code: 0\n[]', 'Exit code: 0\nnull', 'Exit code: 0\n[{"name":"echo"}]', 'Exit code: 0\n{'])('rejects a failed or incomplete native catalog: %j', (result) => {
    expect(() => parseLettaMcpCatalog(capturedReceipt({ stdout: result }))).toThrow()
  })
})
