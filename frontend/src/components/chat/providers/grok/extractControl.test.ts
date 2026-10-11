import { describe, expect, it } from 'vitest'
import { GROK_FOLDER_TRUST_OPTIONS, grokElicitation, grokExtractControl, grokFolderTrustLabel } from './extractControl'

describe('grokExtractControl', () => {
  it('draws a plan approval with the plan Grok sent', () => {
    expect(grokExtractControl({ payload: { method: '_x.ai/exit_plan_mode', params: { toolCallId: 'c', planContent: '# Plan\n\n1. Do X' } } }))
      .toEqual({ kind: 'plan', text: '# Plan\n\n1. Do X' })
  })

  it('draws an empty plan with no text', () => {
    expect(grokExtractControl({ payload: { method: '_x.ai/exit_plan_mode', params: { toolCallId: 'c', planContent: null } } })).toEqual({ kind: 'plan' })
  })

  it('draws folder trust as a permission with its two answers', () => {
    const control = grokExtractControl({ payload: { method: '_x.ai/folder_trust/request', params: { sessionId: 's', cwd: '/w/sub', workspace: '/w', configKinds: ['mcp', 'hooks'] } } })
    if (control?.kind !== 'permission')
      throw new Error('folder trust is a permission')
    expect(control.permission.title).toBe('Trust the workspace /w')
    expect(control.permission.reason).toContain('(mcp, hooks)')
    expect(control.permission.options).toEqual([...GROK_FOLDER_TRUST_OPTIONS])
  })

  it('titles the card with the working directory when the request states no workspace', () => {
    const control = grokExtractControl({ payload: { method: '_x.ai/folder_trust/request', params: { cwd: '/w', configKinds: [] } } })
    expect(control?.kind === 'permission' && control.permission.title).toBe('Trust the workspace /w')
    expect(control?.kind === 'permission' && control.permission.reason).not.toContain('(')
  })

  it('titles the card with no path when the request states neither workspace nor directory', () => {
    const control = grokExtractControl({ payload: { method: '_x.ai/folder_trust/request', params: {} } })
    expect(control?.kind === 'permission' && control.permission.title).toBe('Trust this workspace')
    expect(control?.kind === 'permission' && control.permission.options).toEqual([...GROK_FOLDER_TRUST_OPTIONS])
  })

  it('lists only the configuration kinds that are words', () => {
    const control = grokExtractControl({ payload: { method: '_x.ai/folder_trust/request', params: { workspace: '/w', configKinds: ['mcp', '', 7, null, 'hooks'] } } })
    expect(control?.kind === 'permission' && control.permission.reason).toContain('(mcp, hooks)')
  })

  it('draws a plan with no text when the plan is empty or no string', () => {
    expect(grokExtractControl({ payload: { method: '_x.ai/exit_plan_mode', params: { planContent: '' } } })).toEqual({ kind: 'plan' })
    expect(grokExtractControl({ payload: { method: '_x.ai/exit_plan_mode', params: { planContent: 7 } } })).toEqual({ kind: 'plan' })
    expect(grokExtractControl({ payload: { method: '_x.ai/exit_plan_mode' } })).toEqual({ kind: 'plan' })
  })

  it('reads a tool permission through the shared reader', () => {
    const control = grokExtractControl({ payload: { method: 'session/request_permission', params: {
      toolCall: { toolCallId: 'c', kind: 'execute', title: 'Execute `ls`', rawInput: { variant: 'Bash', command: 'ls' } },
      options: [{ optionId: 'allow-once', name: 'Yes, proceed', kind: 'allow_once' }, { optionId: 'reject-once', name: 'No', kind: 'reject_once' }],
    } } })
    expect(control?.kind).toBe('permission')
    expect(control?.kind === 'permission' && control.permission.command).toBe('ls')
  })

  it('answers null for a request that is none of these', () => {
    expect(grokExtractControl({ payload: { method: '_x.ai/unknown' } })).toBeNull()
  })
})

describe('grokFolderTrustLabel', () => {
  it('answers the words of each button, and nothing for another word', () => {
    expect(grokFolderTrustLabel('trust')).toBe('Trust this workspace')
    expect(grokFolderTrustLabel('reject')).toBe('Do not trust')
    expect(grokFolderTrustLabel('maybe')).toBeUndefined()
  })
})

describe('grokElicitation', () => {
  it('reads a URL elicitation', () => {
    expect(grokElicitation({ method: '_x.ai/mcp/elicit', params: { serverName: 'auth', message: 'Sign in', mode: 'url', url: 'https://example.com/login', elicitationId: 'e' } }))
      .toEqual({ mode: 'url', message: 'Sign in', server: 'auth', schema: undefined, url: 'https://example.com/login', title: '', description: '' })
  })

  it('defaults the mode to a form', () => {
    expect(grokElicitation({ method: '_x.ai/mcp/elicit', params: {} })?.mode).toBe('form')
  })

  it('still reads the protocol elicitation, and nothing else', () => {
    expect(grokElicitation({ method: 'elicitation/create', params: { message: 'Hi', server: 's' } })?.message).toBe('Hi')
    expect(grokElicitation({ method: 'session/request_permission' })).toBeUndefined()
  })
})
