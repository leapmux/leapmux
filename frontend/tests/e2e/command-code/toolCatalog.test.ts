import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { describe, expect, it } from 'vitest'
import { commandCodeLoadedToolNames, commandCodeToolCatalog } from './toolCatalog'

function request(system: string): MockModelRequestRecord {
  return { protocol: 'openai-chat-completions', path: '/v1/chat/completions', body: { tools: [{ type: 'function', function: { name: 'shell_command' } }], messages: [{ role: 'system', content: system }] } }
}

describe('commandCodeToolCatalog', () => {
  it('combines actual attached and deferred native descriptors', () => {
    const system = '# Additional tools\n\nYou HAVE the 2 tools below, in addition to the ones whose schemas are\nattached to this request.\n- task_create{subject: string, description: string}: Create a task.\n- task_update{taskId: string, status?: string}: Update a task.'
    expect(commandCodeToolCatalog(request(system))).toEqual(['shell_command', 'task_create', 'task_update'])
  })

  it.each([
    'No additional tool descriptor block.',
    '# Additional tools\nYou HAVE the 2 tools below\n- task_create{subject: string}: Create a task.',
    '# Additional tools\nYou HAVE the 2 tools below\n- task_create{subject: string}: Create a task.\n- task_create{subject: string}: Repeated task.',
    '# Additional tools\nYou HAVE the 1 tools below\n- shell_command{command: string}: Repeated attached tool.',
  ])('rejects incomplete or repeated native descriptors', (system) => {
    expect(() => commandCodeToolCatalog(request(system))).toThrow()
  })
})

// Source: Command Code 1.74.1 (dist/cli.mjs, createSearchToolsTool). `select:<name>` runs a fuzzy
// `catalog.search` for each name and keeps the best match, and renderSchema writes `### <name>`
// above each loaded schema. The tool answers "No deferred tool matched" only when the search
// finds nothing, so a lookup of a missing tool can load a tool with another name.
describe('commandCodeLoadedToolNames', () => {
  const schema = (name: string) => `### ${name}\nA description with ### in the middle.\n\nParameters:\n\`\`\`json\n{\n  "type": "object"\n}\n\`\`\``
  const loaded = (...names: string[]) => `Loaded ${names.length} tool schema(s). Call them directly by name from now on, using exactly these parameters.\n\n${names.map(schema).join('\n\n')}`

  it('reads the name above each loaded schema', () => {
    expect(commandCodeLoadedToolNames(loaded('sleep'))).toEqual(['sleep'])
    expect(commandCodeLoadedToolNames(loaded('shell_output', 'task_create'))).toEqual(['shell_output', 'task_create'])
  })

  it('reads no name from a lookup that matched nothing', () => {
    expect(commandCodeLoadedToolNames('No deferred tool matched "select:REPL". The names available to load are listed under "Additional tools" in your system prompt.')).toEqual([])
  })

  it('reads a header only at the start of a line', () => {
    expect(commandCodeLoadedToolNames('Loaded 1 tool schema(s).\n\n### sleep\nSee also ### ask_user_question in a sentence.')).toEqual(['sleep'])
  })

  it('reads no name from empty text', () => {
    expect(commandCodeLoadedToolNames('')).toEqual([])
  })

  // A header form that a later build changes would load a tool and yield no name, and a lookup
  // of an absent tool would then pass. The result states how many schemas it loaded, so a name
  // count that differs from that number is a result that this reader cannot read.
  it.each([
    ['a header form that changed', 'Loaded 1 tool schema(s).\n\n## REPL\nA description.'],
    ['fewer names than the stated count', `${loaded('sleep')}\n`.replace('Loaded 1', 'Loaded 2')],
    ['more names than the stated count', loaded('sleep', 'task_create').replace('Loaded 2', 'Loaded 1')],
    ['a header with no stated count', '### sleep\nA description.'],
    ['a lookup that matched nothing and still lists a name', 'No deferred tool matched "select:REPL".\n\n### sleep\nA description.'],
  ])('refuses a result with %s', (_name, result) => {
    expect(() => commandCodeLoadedToolNames(result)).toThrow('Command Code load_tools result')
  })
})
