import { describe, expect, it } from 'vitest'
import { MIMO_PERMISSION_OPTIONS, mimoExtractControl } from './extractControl'

function permission(properties: Record<string, unknown>) {
  return { type: 'permission.asked', properties: { id: 'per_1', sessionID: 'ses_1', ...properties }, request: { tool_name: String(properties.permission ?? ''), tool_use_id: 'call-1' } }
}

describe('mimoExtractControl', () => {
  it('reads a shell permission with its command and what always would approve', () => {
    expect(mimoExtractControl({ payload: permission({ permission: 'bash', patterns: ['rm -rf build'], always: ['rm *'], metadata: {} }) })).toEqual({
      kind: 'permission',
      permission: {
        title: 'bash',
        command: 'rm -rf build',
        input: { always: ['rm *'] },
        options: [...MIMO_PERMISSION_OPTIONS],
      },
    })
  })

  it('reads the command of a delete from the metadata', () => {
    const control = mimoExtractControl({ payload: permission({ permission: 'bash_delete', patterns: ['rm'], metadata: { command: 'rm -rf /tmp/x', deletes: ['/tmp/x'] } }) })
    expect(control?.kind === 'permission' && control.permission.command).toBe('rm -rf /tmp/x')
    expect(control?.kind === 'permission' && control.permission.input).toEqual({ command: 'rm -rf /tmp/x', deletes: ['/tmp/x'] })
  })

  // MiMo stores nothing for an `always` answer to a request whose always list is
  // empty, and it reads `always` as `once` for a delete, which must ask each time.
  // An offer of "Always allow" there would save an answer that MiMo never keeps.
  it('offers no always answer for a delete, even with an always list, and states no always list', () => {
    const control = mimoExtractControl({ payload: permission({ permission: 'bash_delete', patterns: ['rm -f x'], always: ['rm *'], metadata: { command: 'rm -f x' } }) })
    expect(control?.kind === 'permission' && control.permission.options.map(option => option.optionId)).toEqual(['once', 'reject'])
    expect(control?.kind === 'permission' && control.permission.input).toEqual({ command: 'rm -f x' })
  })

  it.each([
    ['an empty always list', { always: [] }],
    ['no always list', {}],
    ['an always list that holds no pattern', { always: [''] }],
  ])('offers no always answer for a permission with %s', (_name, fields) => {
    const control = mimoExtractControl({ payload: permission({ permission: 'external_directory', patterns: ['/etc/*'], metadata: {}, ...fields }) })
    expect(control?.kind === 'permission' && control.permission.options.map(option => option.optionId)).toEqual(['once', 'reject'])
    expect(control?.kind === 'permission' && control.permission.input).toEqual({ patterns: ['/etc/*'] })
  })

  it('states the patterns of a permission that is not a command', () => {
    const control = mimoExtractControl({ payload: permission({ permission: 'external_directory', patterns: ['/etc/*'], always: ['/etc/*'], metadata: { filepath: '/etc/hosts' } }) })
    expect(control).toEqual({
      kind: 'permission',
      permission: {
        title: 'external_directory',
        input: { patterns: ['/etc/*'], always: ['/etc/*'], filepath: '/etc/hosts' },
        options: [...MIMO_PERMISSION_OPTIONS],
      },
    })
  })

  // A shell permission states one pattern for each command of a compound line. With
  // no command in the metadata, the patterns ARE the command, one to a line.
  it('joins the patterns of a shell permission into the command when the metadata states none', () => {
    const control = mimoExtractControl({ payload: permission({ permission: 'bash', patterns: ['cd build', 'rm -rf out'], always: ['cd *', 'rm *'] }) })
    expect(control).toEqual({
      kind: 'permission',
      permission: {
        title: 'bash',
        command: 'cd build\nrm -rf out',
        input: { always: ['cd *', 'rm *'] },
        options: [...MIMO_PERMISSION_OPTIONS],
      },
    })
  })

  // The metadata states the command MiMo will run, which can differ from the
  // patterns MiMo keeps for `always`.
  it('prefers the command the metadata states over the patterns', () => {
    const control = mimoExtractControl({ payload: permission({ permission: 'bash', patterns: ['rm'], always: ['rm *'], metadata: { command: 'rm -rf build' } }) })
    expect(control?.kind === 'permission' && control.permission.command).toBe('rm -rf build')
  })

  // Only a shell permission states a command. Another permission keeps its
  // patterns as arguments and draws no command, whatever its metadata holds.
  it('draws no command for a permission that is not a shell command', () => {
    const control = mimoExtractControl({ payload: permission({ permission: 'webfetch', patterns: ['https://example.com/*'], always: ['https://example.com/*'], metadata: { command: 'fetch' } }) })
    expect(control).toEqual({
      kind: 'permission',
      permission: {
        title: 'webfetch',
        input: { patterns: ['https://example.com/*'], always: ['https://example.com/*'], command: 'fetch' },
        options: [...MIMO_PERMISSION_OPTIONS],
      },
    })
  })

  it('keeps only the text patterns', () => {
    const control = mimoExtractControl({ payload: permission({ permission: 'external_directory', patterns: ['/etc/*', 7, null], always: [3, '/etc/*'] }) })
    expect(control?.kind === 'permission' && control.permission.input).toEqual({ patterns: ['/etc/*'], always: ['/etc/*'] })
  })

  // A request that lost its properties still reaches the banner, with no answer
  // that MiMo would not keep.
  it('reads a permission that states no properties', () => {
    expect(mimoExtractControl({ payload: { type: 'permission.asked' } })).toEqual({
      kind: 'permission',
      permission: {
        title: '',
        input: {},
        options: MIMO_PERMISSION_OPTIONS.filter(option => option.optionId !== 'always'),
      },
    })
  })

  it('offers MiMo\'s own three answers', () => {
    expect(MIMO_PERMISSION_OPTIONS.map(option => [option.optionId, option.kind])).toEqual([
      ['once', 'allow_once'],
      ['always', 'allow_always'],
      ['reject', 'reject_once'],
    ])
  })

  it('reads a plan approval with the plan the worker read', () => {
    const payload = { type: 'question.asked', properties: { id: 'que_1', questions: [{ key: 'plan_exit' }] }, request: { tool_name: 'plan_exit' }, plan: '# Plan' }
    expect(mimoExtractControl({ payload })).toEqual({ kind: 'plan', text: '# Plan' })
    expect(mimoExtractControl({ payload: { ...payload, plan: '' } })).toEqual({ kind: 'plan' })
  })

  // The worker leaves the plan text out when it cannot read the file: a file past
  // its size limit, a file that is not UTF-8, or a path that is not Markdown. The
  // banner then states where the plan is, or MiMo's own question.
  describe('a plan approval without the plan text', () => {
    const question = 'Plan at .mimocode/plans/1-eager-canyon.md is complete. Would you like to switch to the build agent and start implementing?'
    const approval = (entry: Record<string, unknown>) => ({ type: 'question.asked', properties: { id: 'que_1', questions: [{ key: 'plan_exit', ...entry }] }, request: { tool_name: 'plan_exit' } })

    it('states the plan file', () => {
      expect(mimoExtractControl({ payload: approval({ params: { plan: '.mimocode/plans/1-eager-canyon.md' }, question }) }))
        .toEqual({ kind: 'plan', details: ['Plan file: .mimocode/plans/1-eager-canyon.md'] })
    })

    it('states MiMo\'s question when the approval gives no file', () => {
      expect(mimoExtractControl({ payload: approval({ params: {}, question }) })).toEqual({ kind: 'plan', details: [question] })
      expect(mimoExtractControl({ payload: approval({ params: { plan: '  ' }, question }) })).toEqual({ kind: 'plan', details: [question] })
    })

    it('states the plan text alone when the worker read it', () => {
      expect(mimoExtractControl({ payload: { ...approval({ params: { plan: 'p.md' }, question }), plan: '# Plan' } })).toEqual({ kind: 'plan', text: '# Plan' })
    })

    it('trims the plan file and the question', () => {
      expect(mimoExtractControl({ payload: approval({ params: { plan: '  p.md\n' } }) })).toEqual({ kind: 'plan', details: ['Plan file: p.md'] })
      expect(mimoExtractControl({ payload: approval({ question: `  ${question}\n` }) })).toEqual({ kind: 'plan', details: [question] })
    })

    it('states no details when the approval gives neither a file nor a question', () => {
      expect(mimoExtractControl({ payload: approval({ params: { plan: ' ' }, question: '  ' }) })).toEqual({ kind: 'plan' })
      expect(mimoExtractControl({ payload: { type: 'question.asked', properties: { questions: ['plan_exit'] }, request: { tool_name: 'plan_exit' } } })).toEqual({ kind: 'plan' })
      expect(mimoExtractControl({ payload: { type: 'question.asked', request: { tool_name: 'plan_exit' } } })).toEqual({ kind: 'plan' })
    })

    // The worker states the plan text as a string. Any other value is no text, and
    // the banner falls back to where the plan is.
    it('reads a plan field that is not text as no plan text', () => {
      expect(mimoExtractControl({ payload: { ...approval({ params: { plan: 'p.md' } }), plan: { text: '# Plan' } } })).toEqual({ kind: 'plan', details: ['Plan file: p.md'] })
    })
  })

  it('reads no other request', () => {
    expect(mimoExtractControl({ payload: { type: 'question.asked', properties: {}, request: { tool_name: 'question' } } })).toBeNull()
    expect(mimoExtractControl({ payload: {} })).toBeNull()
  })
})
