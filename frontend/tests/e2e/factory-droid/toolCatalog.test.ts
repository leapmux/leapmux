import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { describe, expect, it } from 'vitest'
import { droidCompleteToolCatalog, droidLoadedToolSchemas, droidScriptExecutors } from './toolCatalog'

const descriptor = (name: string, properties: Record<string, unknown> = { command: { type: 'string' } }, description = 'Run a shell command.') => ({ type: 'function', function: { name, description, parameters: { type: 'object', properties } } })
const request = (tools: unknown, content = ''): MockModelRequestRecord => ({ protocol: 'openai-chat-completions', path: '/v1/chat/completions', body: { tools, messages: [{ role: 'user', content }] } })
const reminder = (names: string[]) => `<system-reminder>\nThe tools listed below are available in this environment, but their schemas may be omitted from the current tool list to save context.\nLoad a listed tool with ToolSearch: pass query "select:<name>[,<name>...]".\n\nDeferred tools:\n${names.join('\n')}\n</system-reminder>`

describe('droidCompleteToolCatalog', () => {
  it('keeps native descriptions and schemas in a current-only inventory', () => {
    expect(droidCompleteToolCatalog(request([descriptor('Execute')]))).toEqual({ current: [{ name: 'Execute', description: 'Run a shell command.', inputSchema: { type: 'object', properties: { command: { type: 'string' } } } }], deferred: [] })
  })

  it('reads the complete deferred name list inside its native delimiters', () => {
    expect(droidCompleteToolCatalog(request([descriptor('Execute')], reminder(['server___inspect', 'server___lookup']))).deferred).toEqual(['server___inspect', 'server___lookup'])
  })

  it.each([
    { tools: [] },
    { tools: [descriptor('Execute'), descriptor('Execute')] },
    { tools: [{ name: 'Execute', description: 'Shell' }] },
    { tools: [descriptor('Execute')], content: reminder(['Execute']) },
    { tools: [descriptor('Execute')], content: reminder(['tool', 'tool']) },
    { tools: [descriptor('Execute')], content: reminder(['tool']).replace('</system-reminder>', '') },
    { tools: [descriptor('Execute')], content: reminder(['tool']).replace('<system-reminder>', '<other>') },
    { tools: [descriptor('Execute')], content: reminder(['tool']).replace('schemas may be omitted from the current tool list', 'A partial tool list') },
    { tools: [descriptor('Execute')], content: reminder(['tool']).replace('select:<name>[,<name>...]', 'keyword search') },
    { tools: [descriptor('Execute')], content: reminder(['tool']) + reminder(['other']) },
  ])('rejects an incomplete or ambiguous inventory: %j', ({ tools, content }) => {
    expect(() => droidCompleteToolCatalog(request(tools, content))).toThrow()
  })
})

describe('droidScriptExecutors', () => {
  it('detects an interpreter by its native capability and source argument', () => {
    const catalog = droidCompleteToolCatalog(request([descriptor('native_runner', { source: { type: 'string' } }, 'Run JavaScript code in a persistent interpreter.')]))
    expect(droidScriptExecutors(catalog.current).map(tool => tool.name)).toEqual(['native_runner'])
    expect(droidScriptExecutors(droidCompleteToolCatalog(request([descriptor('Execute')])).current)).toEqual([])
  })

  it('detects the installed native script tool and keeps its complete argument schema', () => {
    const script = {
      type: 'function',
      function: {
        name: 'Script',
        description: 'Run JavaScript to orchestrate multiple tool calls with loops, conditions, and parallelism.',
        parameters: {
          type: 'object',
          properties: { script: { type: 'string' }, waitForMs: { type: 'number', minimum: 0 } },
          required: ['script'],
          additionalProperties: false,
        },
      },
    }
    const catalog = droidCompleteToolCatalog(request([descriptor('Execute'), script]))
    expect(droidScriptExecutors(catalog.current)).toEqual([{
      name: 'Script',
      description: script.function.description,
      inputSchema: script.function.parameters,
    }])
  })
})

describe('droidLoadedToolSchemas', () => {
  const before = () => droidCompleteToolCatalog(request([descriptor('Execute')], reminder(['native_runner'])))
  it('requires the actual loaded descriptor rather than a search response string', () => {
    const after = droidCompleteToolCatalog(request([descriptor('Execute'), descriptor('native_runner', { source: { type: 'string' } }, 'Run JavaScript code.')]))
    expect(droidLoadedToolSchemas(before(), after, ['native_runner'])[0]?.inputSchema).toEqual({ type: 'object', properties: { source: { type: 'string' } } })
  })

  it.each([{ selected: [] }, { selected: ['missing'] }, { selected: ['native_runner', 'native_runner'] }, { selected: ['native_runner'] }])('rejects an absent or uncorrelated schema receipt: %j', ({ selected }) => {
    expect(() => droidLoadedToolSchemas(before(), before(), selected)).toThrow()
  })
})
