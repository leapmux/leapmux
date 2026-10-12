import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { describe, expect, it } from 'vitest'
import { nativeCodeExecutionSchema } from '../helpers/nativeCodeExecution'
import { DROID_SCRIPT_ARGUMENTS, droidCompleteToolCatalog, droidLoadedToolSchemas, droidScriptExecutors } from './toolCatalog'

const descriptor = (name: string, properties: Record<string, unknown> = { command: { type: 'string' } }, description = 'Run a shell command.') => ({ type: 'function', function: { name, description, parameters: { type: 'object', properties } } })
const request = (tools: unknown, content = ''): MockModelRequestRecord => ({ protocol: 'openai-chat-completions', path: '/v1/chat/completions', body: { tools, messages: [{ role: 'user', content }] } })
const reminder = (names: string[]) => `<system-reminder>\nThe tools listed below are available in this environment, but their schemas may be omitted from the current tool list to save context.\nLoad a listed tool with ToolSearch: pass query "select:<name>[,<name>...]".\n\nDeferred tools:\n${names.join('\n')}\n</system-reminder>`

// The Script descriptor that Droid 0.233.0 sent in a captured request.
const CAPTURED_SCRIPT_DESCRIPTOR = {
  type: 'function',
  function: {
    name: 'Script',
    description: 'Run JavaScript to orchestrate multiple tool calls with loops, conditions, and parallelism.',
    parameters: {
      type: 'object',
      properties: {
        script: { type: 'string', description: 'Inline JavaScript source' },
        inputs: { type: 'object', additionalProperties: { type: 'string' }, description: 'Strings the script reads as `inputs.<name>`.' },
      },
      required: ['script'],
      additionalProperties: false,
    },
  },
}

describe('droidCompleteToolCatalog', () => {
  it('keeps native descriptions and schemas in a current-only inventory', () => {
    expect(droidCompleteToolCatalog(request([descriptor('Execute')]))).toEqual({ current: [{ name: 'Execute', description: 'Run a shell command.', inputSchema: { type: 'object', properties: { command: { type: 'string' } } } }], deferred: [] })
  })

  it('reads the complete deferred name list inside its native delimiters', () => {
    expect(droidCompleteToolCatalog(request([descriptor('Execute')], reminder(['server___inspect', 'server___lookup']))).deferred).toEqual(['server___inspect', 'server___lookup'])
  })

  it.each([
    [{ tools: [] }, 'The native Droid request contains no current tool inventory.'],
    [{ tools: [descriptor('Execute'), descriptor('Execute')] }, 'The native Droid catalog contains duplicate tool names.'],
    [{ tools: [{ name: 'Execute', description: 'Shell' }] }, 'The native Droid catalog contains an incomplete tool descriptor.'],
    [{ tools: [descriptor('Execute')], content: reminder(['Execute']) }, 'The native Droid current and deferred inventories contain a duplicate tool.'],
    [{ tools: [descriptor('Execute')], content: reminder(['tool', 'tool']) }, 'The native Droid current and deferred inventories contain a duplicate tool.'],
    [{ tools: [descriptor('Execute')], content: reminder(['tool']).replace('</system-reminder>', '') }, 'The native Droid deferred inventory is truncated or has invalid delimiters.'],
    [{ tools: [descriptor('Execute')], content: reminder(['tool']).replace('<system-reminder>', '<other>') }, 'The native Droid deferred inventory is truncated or has invalid delimiters.'],
    [{ tools: [descriptor('Execute')], content: reminder(['tool']).replace('schemas may be omitted from the current tool list', 'A partial tool list') }, 'The native Droid deferred inventory lacks its native completeness statement.'],
    [{ tools: [descriptor('Execute')], content: reminder(['tool']).replace('select:<name>[,<name>...]', 'keyword search') }, 'The native Droid deferred inventory lacks its native completeness statement.'],
    [{ tools: [descriptor('Execute')], content: reminder(['tool']) + reminder(['other']) }, 'The native Droid request contains multiple deferred inventories.'],
  ])('rejects an incomplete or ambiguous inventory: %j', ({ tools, content }: { tools: unknown, content?: string }, error: string) => {
    expect(() => droidCompleteToolCatalog(request(tools, content))).toThrow(error)
  })

  it('rejects a tool entry that is not an object', () => {
    expect(() => droidCompleteToolCatalog(request([descriptor('Execute'), 'Execute']))).toThrow('The native model tool catalog contains an invalid entry.')
  })
})

describe('droidScriptExecutors', () => {
  it('detects an interpreter by its native capability and source argument', () => {
    const catalog = droidCompleteToolCatalog(request([descriptor('native_runner', { source: { type: 'string' } }, 'Run JavaScript code in a persistent interpreter.')]))
    expect(droidScriptExecutors(catalog.current).map(tool => tool.name)).toEqual(['native_runner'])
    expect(droidScriptExecutors(droidCompleteToolCatalog(request([descriptor('Execute')])).current)).toEqual([])
  })

  it('detects the installed native script tool and keeps its complete argument schema', () => {
    const catalog = droidCompleteToolCatalog(request([descriptor('Execute'), CAPTURED_SCRIPT_DESCRIPTOR]))
    expect(droidScriptExecutors(catalog.current)).toEqual([{
      name: 'Script',
      description: CAPTURED_SCRIPT_DESCRIPTOR.function.description,
      inputSchema: CAPTURED_SCRIPT_DESCRIPTOR.function.parameters,
    }])
  })
})

describe('DROID_SCRIPT_ARGUMENTS', () => {
  it('states the argument types of the descriptor that Droid sends the model', () => {
    expect(nativeCodeExecutionSchema(request([CAPTURED_SCRIPT_DESCRIPTOR]), 'Script', DROID_SCRIPT_ARGUMENTS).required).toEqual(['script'])
  })

  // The parser of Droid accepts waitForMs, but the schema that the model sees omits it.
  it('does not state a waitForMs field, which no catalog lists', () => {
    expect(() => nativeCodeExecutionSchema(request([CAPTURED_SCRIPT_DESCRIPTOR]), 'Script', { script: 'string', waitForMs: 'number' })).toThrow('waitForMs has no number schema')
  })
})

describe('droidLoadedToolSchemas', () => {
  const before = () => droidCompleteToolCatalog(request([descriptor('Execute')], reminder(['native_runner'])))
  it('requires the actual loaded descriptor rather than a search response string', () => {
    const after = droidCompleteToolCatalog(request([descriptor('Execute'), descriptor('native_runner', { source: { type: 'string' } }, 'Run JavaScript code.')]))
    expect(droidLoadedToolSchemas(before(), after, ['native_runner'])[0]?.inputSchema).toEqual({ type: 'object', properties: { source: { type: 'string' } } })
  })

  const NOT_DISTINCT = 'The Droid schema receipt requires distinct announced deferred names.'
  it.each([
    [{ selected: [] }, NOT_DISTINCT],
    [{ selected: ['missing'] }, NOT_DISTINCT],
    [{ selected: ['native_runner', 'native_runner'] }, NOT_DISTINCT],
    [{ selected: ['native_runner'] }, 'The native Droid search supplied no loaded schema for native_runner.'],
  ])('rejects an absent or uncorrelated schema receipt: %j', ({ selected }, error) => {
    expect(() => droidLoadedToolSchemas(before(), before(), selected)).toThrow(error)
  })
})
