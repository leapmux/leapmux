import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { describe, expect, it } from 'vitest'
import { commandCodeToolCatalog } from './toolCatalog'

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
