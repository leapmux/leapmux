import { describe, expect, it } from 'vitest'
import { allowScopePillOptions, decisionLabel, layoutPermissionOptions, resolvePermissionOption } from '../../controls/permissionOptions'
import { KIRO_ALWAYS_ALLOW, KIRO_ALWAYS_DENY, kiroElicitation, kiroExtractControl, kiroPermissionOptions } from './extractControl'

const OPTIONS = [
  { optionId: 'accept', name: 'Allow', kind: 'allow_once' },
  { optionId: 'always-accept', name: 'Always allow', kind: 'allow_always' },
  { optionId: 'reject', name: 'Deny', kind: 'reject_once' },
  { optionId: 'always-reject', name: 'Always deny', kind: 'reject_always' },
]

/** Kiro's permission request for a shell command, as the probe recorded it. */
const SHELL = {
  jsonrpc: '2.0',
  id: 5,
  method: 'session/request_permission',
  params: {
    sessionId: 's',
    toolCall: { toolCallId: 'run_command_t_sh', status: 'pending', title: 'echo v3-shell' },
    options: OPTIONS,
    _meta: { kiro: { toolId: 'run_command', command: 'echo v3-shell', consent: { capability: 'shell', resource: 'echo v3-shell', askType: 'implicit', workspaceRoot: '/w' }, consentRound: 1 } },
  },
}

/** Kiro's review of a Supervised turn, as the probe recorded it. */
const TURN_APPROVAL = {
  jsonrpc: '2.0',
  id: 6,
  method: 'session/request_permission',
  params: {
    sessionId: 's',
    toolCall: { toolCallId: 'turn_approval_1', status: 'pending', title: 'Review changes' },
    options: [{ optionId: 'accept', name: 'Accept changes', kind: 'allow_once' }, { optionId: 'reject', name: 'Reject changes', kind: 'reject_once' }],
    _meta: { kiro: { type: 'turn_approval', executionId: 'e', files: [{ path: '/w/supervised.txt', toolCallId: 'w_sup' }] } },
  },
}

describe('kiroPermissionOptions', () => {
  it('adds the wider scopes after each of Kiro\'s own always options', () => {
    expect(kiroPermissionOptions(OPTIONS, { consent: { workspaceRoot: '/w' } })).toEqual([
      OPTIONS[0],
      { optionId: 'always-accept', name: KIRO_ALWAYS_ALLOW['always-accept'].name, kind: 'allow_always', scope: 'session' },
      { optionId: 'always-accept-workspace', name: 'Always allow in this workspace', kind: 'allow_always', scope: 'workspace' },
      { optionId: 'always-accept-user', name: 'Always allow everywhere', kind: 'allow_always', scope: 'user' },
      OPTIONS[2],
      { optionId: 'always-reject', name: KIRO_ALWAYS_DENY['always-reject'].name, kind: 'reject_always', scope: 'session' },
      { optionId: 'always-reject-workspace', name: 'Always deny in this workspace', kind: 'reject_always', scope: 'workspace' },
      { optionId: 'always-reject-user', name: 'Always deny everywhere', kind: 'reject_always', scope: 'user' },
    ])
  })

  it('sends Deny at the scope that the selected pill states', () => {
    const layout = layoutPermissionOptions(kiroPermissionOptions(OPTIONS, { consent: { workspaceRoot: '/w' } }))
    const denies = Object.fromEntries((layout.allowScope ?? []).map(pill => [pill.optionId, resolvePermissionOption(layout, 'reject', pill.optionId)?.optionId]))
    expect(denies).toEqual({
      'accept': 'reject',
      'always-accept': 'always-reject',
      'always-accept-workspace': 'always-reject-workspace',
      'always-accept-user': 'always-reject-user',
    })
    // The pills reach every always option, so the row draws no extra button.
    expect(layout.additional).toEqual([])
  })

  it('states each scope, so the scope pills read Session, Workspace and Always', () => {
    const layout = layoutPermissionOptions(kiroPermissionOptions(OPTIONS, { consent: { workspaceRoot: '/w' } }))
    expect(allowScopePillOptions(layout.allowScope ?? [])?.map(pill => pill.label)).toEqual(['Once', 'Session', 'Workspace', 'Always'])
  })

  it('leaves the workspace scope out when the request states no workspace root', () => {
    for (const meta of [undefined, { consent: {} }, { consent: { workspaceRoot: '  ' } }]) {
      expect(kiroPermissionOptions(OPTIONS, meta).map(option => option.optionId), JSON.stringify(meta))
        .toEqual(['accept', 'always-accept', 'always-accept-user', 'reject', 'always-reject', 'always-reject-user'])
    }
  })

  // The option id alone is not Kiro's always rule: the kind must say so too.
  it('adds nothing for an option that carries Kiro\'s id under another kind', () => {
    const options = [{ optionId: 'always-accept', name: 'Allow', kind: 'allow_once' }, { optionId: 'always-reject', name: 'Deny', kind: 'reject_once' }]
    expect(kiroPermissionOptions(options, { consent: { workspaceRoot: '/w' } })).toBe(options)
  })

  it('adds nothing to a request that offers no always option', () => {
    const options = [OPTIONS[0]!, OPTIONS[2]!]
    expect(kiroPermissionOptions(options, { consent: { workspaceRoot: '/w' } })).toBe(options)
  })

  it('states the session scope of an always-deny that no scope pill qualifies', () => {
    // Kiro offers no always-accept for an explicit ask, so the row draws no scope
    // pills, and the always-deny is a button of its own that states its duration.
    const options = [OPTIONS[0]!, OPTIONS[2]!, OPTIONS[3]!]
    expect(kiroPermissionOptions(options, { consent: { workspaceRoot: '/w' } })).toEqual([
      OPTIONS[0],
      OPTIONS[2],
      { optionId: 'always-reject', name: 'Always deny for this session', kind: 'reject_always', scope: 'session' },
    ])
  })
})

describe('kiroExtractControl', () => {
  it('draws a shell permission with its command and every scope', () => {
    const control = kiroExtractControl({ payload: SHELL })
    if (control?.kind !== 'permission')
      throw new Error('a tool permission is a permission')
    expect(control.permission.title).toBe('echo v3-shell')
    expect(control.permission.command).toBe('echo v3-shell')
    expect(control.permission.options.map(option => option.optionId)).toContain('always-accept-workspace')
  })

  it('draws a turn review with the files it holds', () => {
    const control = kiroExtractControl({ payload: TURN_APPROVAL })
    if (control?.kind !== 'permission')
      throw new Error('a turn review is a permission')
    expect(control.permission.title).toBe('Review changes')
    expect(control.permission.reason).toContain('- /w/supervised.txt')
    expect(control.permission.options.map(option => option.name)).toEqual(['Accept changes', 'Reject changes'])
  })

  it('names the buttons of the decision row in a turn review', () => {
    const control = kiroExtractControl({ payload: TURN_APPROVAL })
    if (control?.kind !== 'permission')
      throw new Error('a turn review is a permission')
    const layout = layoutPermissionOptions(control.permission.options)
    expect(control.permission.reason).toContain(`${decisionLabel(layout, 'allow')} applies them`)
    expect(control.permission.reason).toContain(`${decisionLabel(layout, 'reject')} restores each file`)
  })

  it('states the review without a list when the request states no file', () => {
    const control = kiroExtractControl({ payload: { ...TURN_APPROVAL, params: { ...TURN_APPROVAL.params, _meta: { kiro: { type: 'turn_approval' } } } } })
    expect(control?.kind === 'permission' && control.permission.reason).not.toContain('- ')
  })

  it('lists only the files that state a path', () => {
    const control = kiroExtractControl({ payload: { ...TURN_APPROVAL, params: { ...TURN_APPROVAL.params, _meta: { kiro: { type: 'turn_approval', files: [{ path: '' }, 'loose', null, { path: '/w/kept.txt' }] } } } } })
    const reason = control?.kind === 'permission' ? control.permission.reason : ''
    expect(reason).toContain(':\n- /w/kept.txt')
    expect(reason?.match(/^- /gm)).toHaveLength(1)
  })

  it('states the review without a list when every file lacks a path', () => {
    const control = kiroExtractControl({ payload: { ...TURN_APPROVAL, params: { ...TURN_APPROVAL.params, _meta: { kiro: { type: 'turn_approval', files: [{ path: '' }] } } } } })
    expect(control?.kind === 'permission' && control.permission.reason).toBe('Kiro holds the file changes of this turn for your review. Allow applies them, and Deny restores each file.')
  })

  // The command the shared reader found is what the call runs, so Kiro's own copy
  // only fills a permission that states none.
  it('keeps the command that the shared reader found over Kiro\'s own copy', () => {
    const payload = { ...SHELL, params: { ...SHELL.params, toolCall: { toolCallId: 'c', status: 'pending', kind: 'execute', title: 'List', rawInput: { command: 'ls -la' } } } }
    const control = kiroExtractControl({ payload })
    expect(control?.kind === 'permission' && control.permission.command).toBe('ls -la')
  })

  it('answers null for a request that is no permission', () => {
    expect(kiroExtractControl({ payload: { method: '_kiro/unknown' } })).toBeNull()
  })
})

describe('kiroElicitation', () => {
  it('reads a form that an MCP server asks for', () => {
    expect(kiroElicitation({
      method: '_kiro/mcp/elicitation',
      params: { sessionId: 's', toolCallId: 'm_ask', elicitation: { mode: 'form', message: 'Pick a color', requestedSchema: { type: 'object' } } },
    })).toEqual({ mode: 'form', message: 'Pick a color', server: '', schema: { type: 'object' }, url: '', title: '', description: '' })
  })

  it('reads a URL elicitation', () => {
    expect(kiroElicitation({ method: '_kiro/mcp/elicitation', params: { elicitation: { mode: 'url', message: 'Sign in', url: 'https://example.com/login', elicitationId: 'e' } } }))
      .toMatchObject({ mode: 'url', message: 'Sign in', url: 'https://example.com/login' })
  })

  it('defaults the mode to a form', () => {
    expect(kiroElicitation({ method: '_kiro/mcp/elicitation', params: {} })?.mode).toBe('form')
  })

  it('still reads the protocol elicitation, and nothing else', () => {
    expect(kiroElicitation({ method: 'elicitation/create', params: { message: 'Hi', server: 's' } })?.message).toBe('Hi')
    expect(kiroElicitation({ method: 'session/request_permission' })).toBeUndefined()
  })
})
