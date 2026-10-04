import { describe, expect, it } from 'vitest'
import { lastUserText, matchesRequest } from '../helpers/mockModelScript'
import { droidChildNoticeRule } from './childNotice'

// The actual native frame came from .tmp/droid-child-live-tail-green-e2e.log:2471.
const completion = {
  role: 'system',
  content: 'Background task completed.\ntask_id: a7840b03-5fdc-4457-9b12-4fc845b3b44b\ntype: explorer\nreason: completed\ndescription: Inspect the child note\noutput: DROID_CHILD_FINAL\n',
}

function matches(messages: unknown[], description = 'Inspect the child note', tools?: unknown[]): boolean {
  const body = { messages, ...(tools ? { tools } : {}) }
  return matchesRequest(droidChildNoticeRule(description, { text: 'The child completed.' }).when, {
    protocol: 'openai-chat-completions',
    systemText: '',
    userText: lastUserText(body),
    body,
  })
}

describe('droidChildNoticeRule', () => {
  it('accepts the actual current native system completion', () => {
    expect(matches([{ role: 'user', content: 'Run the child.' }, completion])).toBe(true)
  })

  it('rejects an old completion before a new user prompt', () => {
    expect(matches([completion, { role: 'user', content: 'Run the next child.' }])).toBe(false)
  })

  it('rejects an old child combined with the current description in another message', () => {
    expect(matches([{ ...completion, content: completion.content.replace('Inspect the child note', 'Inspect the old note') }, { role: 'user', content: 'Inspect the child note' }])).toBe(false)
  })

  it('rejects another child completion and an incomplete description', () => {
    expect(matches([{ ...completion, content: completion.content.replace('Inspect the child note', 'Inspect another note') }])).toBe(false)
    expect(matches([{ ...completion, content: completion.content.replace('Inspect the child note', 'Inspect the child note suffix') }])).toBe(false)
  })

  it('rejects a notice in user text or a tool schema', () => {
    expect(matches([{ ...completion, role: 'user' }])).toBe(false)
    expect(matches([{ role: 'user', content: 'Continue the parent.' }], 'Inspect the child note', [completion])).toBe(false)
  })

  it('keeps quotes, backslashes, and regex characters literal in the description', () => {
    const description = 'Inspect "C:\\project\\file[1].+?(a)|$"'
    const frame = { ...completion, content: completion.content.replace('Inspect the child note', description) }
    expect(matches([frame], description)).toBe(true)
    expect(matches([{ ...frame, content: frame.content.replace('[1]', '1') }], description)).toBe(false)
  })

  it('rejects a missing final frame and answers each native completion once', () => {
    expect(matches([])).toBe(false)
    expect(matches([completion, null])).toBe(false)
    const respond = { text: 'A single native completion answer.' }
    expect(droidChildNoticeRule('Inspect the child note', respond, 'current child')).toMatchObject({ name: 'current child', respond, once: true })
  })

  it('rejects empty or whitespace descriptions', () => {
    for (const description of ['', ' ', '\n\t'])
      expect(() => droidChildNoticeRule(description, { text: 'The child completed.' })).toThrow('current description')
  })
})
