import { describe, expect, it } from 'vitest'
import { lastUserText, matchesRequest } from '../helpers/mockModelScript'
import { diracActModeContinuationRule, diracCurrentModeDetails } from './modeContinuation'

const continuation = '<environment_details>\nThe user switched to ACT MODE. You are now in Act mode.\n\n# Current Mode\nACT MODE\n## EDITING FILES\nNative editing instructions.\n</environment_details>'

function matches(messages: unknown[]) {
  const rule = diracActModeContinuationRule('mode-switch', { text: 'Native continuation' })
  const body = { messages }
  return matchesRequest(rule.when, {
    protocol: 'openai-chat-completions',
    systemText: '',
    userText: lastUserText('openai-chat-completions', body),
    body,
  })
}

describe('diracActModeContinuationRule', () => {
  it('matches the current native mode-change announcement', () => {
    expect(matches([
      {
        role: 'user',
        content: [
          { type: 'text', text: continuation },
        ],
      },
    ])).toBe(true)
  })

  it('refuses an ordinary Act prompt without the mode-change announcement', () => {
    expect(matches([
      { role: 'user', content: '<environment_details>\n# Current Mode\nACT MODE\n</environment_details>' },
    ])).toBe(false)
  })

  it('refuses a historical announcement before a new Plan prompt', () => {
    expect(matches([
      { role: 'user', content: continuation },
      { role: 'assistant', content: 'Completed' },
      { role: 'user', content: '<environment_details>\n# Current Mode\nPLAN MODE\n</environment_details>' },
    ])).toBe(false)
  })

  it('refuses quoted announcements and a missing current user message', () => {
    expect(matches([
      { role: 'user', content: `Quoted native message:\n${continuation}` },
    ])).toBe(false)
    expect(matches([])).toBe(false)
    expect(matches([
      { role: 'assistant', content: continuation },
    ])).toBe(false)
  })
})

describe('diracCurrentModeDetails', () => {
  const plan = '<environment_details>\n# Current Mode\nPLAN MODE\nNative plan instructions.\n</environment_details>'
  function read(messages: unknown[]) {
    return diracCurrentModeDetails({
      protocol: 'openai-chat-completions',
      path: '/v1/chat/completions',
      body: { messages },
    })
  }

  it('reads the native mode-change prelude as the newest explicit mode', () => {
    expect(read([
      { role: 'user', content: plan },
      {
        role: 'user',
        content: [
          { type: 'text', text: continuation },
        ],
      },
    ])).toBe(continuation)
  })

  it('preserves the last explicit Plan while feedback states no new mode', () => {
    expect(read([
      { role: 'user', content: plan },
      {
        role: 'user',
        content: [
          { type: 'text', text: '<feedback>Revise the plan.</feedback>' },
        ],
      },
    ])).toBe(plan)
  })

  it('ignores system and assistant text and quoted environment blocks', () => {
    expect(read([
      { role: 'user', content: plan },
      { role: 'system', content: continuation },
      { role: 'assistant', content: continuation },
      { role: 'user', content: `A quoted mode:\n${continuation}` },
    ])).toBe(plan)
  })

  it('returns no mode for empty or malformed content', () => {
    expect(read([])).toBe('')
    expect(read([
      null,
      {},
      { role: 'user', content: 0 },
      {
        role: 'user',
        content: [
          null,
          {},
          { text: 0 },
        ],
      },
    ])).toBe('')
    expect(diracCurrentModeDetails({ protocol: 'openai-chat-completions', path: '/', body: null })).toBe('')
  })

  it('refuses another model protocol', () => {
    expect(() => diracCurrentModeDetails({ protocol: 'anthropic-messages', path: '/', body: {} })).toThrow('OpenAI-compatible')
  })
})
