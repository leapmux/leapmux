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

// The reasons that the parser gives for a refusal. Stdout that is not one JSON value fails its parse with a SyntaxError.
const NO_NAMED_TOOLS = 'The native Letta MCP catalog must contain named tools and their schemas.'
const UNSUCCESSFUL = 'The native Letta MCP catalog requires a successful CLI receipt.'

describe('parseLettaMcpCatalog', () => {
  it('rejects the obsolete prefix in native CLI stdout', () => {
    const tools = [{ name: 'mcp__echo_probe__echo', inputSchema: { type: 'object', required: ['value'], properties: { value: { type: 'string' } } } }]
    expect(() => parseLettaMcpCatalog(capturedReceipt({ stdout: `Exit code: 0\n${JSON.stringify(tools, null, 2)}\n` }))).toThrow(SyntaxError)
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
    [{ stdout: '' }, SyntaxError],
    [{ stdout: '[]' }, NO_NAMED_TOOLS],
    [{ stdout: 'null' }, NO_NAMED_TOOLS],
    [{ stdout: '[{"name":"echo"}]' }, NO_NAMED_TOOLS],
    [{ stdout: '[{"name":"","inputSchema":{}}]' }, NO_NAMED_TOOLS],
    [{ stdout: '[{"name":"echo","inputSchema":null}]' }, NO_NAMED_TOOLS],
    [{ stdout: '[{"name":"echo","inputSchema":[]}]' }, NO_NAMED_TOOLS],
    [{ stdout: '[' }, SyntaxError],
    [{ stdout: `${JSON.stringify(capturedTools)}\n${capturedWarning}` }, SyntaxError],
    [{ stdout: `${JSON.stringify(capturedTools)}\n${JSON.stringify(capturedTools)}` }, SyntaxError],
    [{ stdout: '', stderr: JSON.stringify(capturedTools) }, SyntaxError],
    [{ exitCode: null }, UNSUCCESSFUL],
    [{ exitCode: -1 }, UNSUCCESSFUL],
    [{ exitCode: 2 }, UNSUCCESSFUL],
    [{ exitCode: 255 }, UNSUCCESSFUL],
    [{ signal: 'SIGTERM', exitCode: null }, UNSUCCESSFUL],
    [{ signal: 'SIGTERM', exitCode: 0 }, UNSUCCESSFUL],
    [{ spawnError: 'spawn /private/native/letta ENOENT', exitCode: 0 }, UNSUCCESSFUL],
  ] satisfies Array<[Partial<LettaMcpCliReceipt>, string | SyntaxErrorConstructor]>)('rejects an unsuccessful or incomplete native stdout receipt: %j', (overrides, error) => {
    expect(() => parseLettaMcpCatalog(capturedReceipt(overrides))).toThrow(error)
  })
  it.each([
    ['', SyntaxError],
    ['[]', NO_NAMED_TOOLS],
    ['Exit code: 2\n[]', SyntaxError],
    ['Exit code: 0\n[]', SyntaxError],
    ['Exit code: 0\nnull', SyntaxError],
    ['Exit code: 0\n[{"name":"echo"}]', SyntaxError],
    ['Exit code: 0\n{', SyntaxError],
  ] as const)('rejects a failed or incomplete native catalog: %j', (result, error) => {
    expect(() => parseLettaMcpCatalog(capturedReceipt({ stdout: result }))).toThrow(error)
  })
})
