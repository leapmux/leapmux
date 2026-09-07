import type { WirePermissionOption } from './permissionOptions'
import { describe, expect, it } from 'vitest'
import {
  allowScopeLabel,
  allowScopePillOptions,
  isRejectPermissionKind,
  layoutPermissionOptions,
  permissionOptionLabel,
  resolvePermissionOption,
  scopeRemembers,
} from './permissionOptions'

function option(optionId: string, kind: string, name = optionId): WirePermissionOption {
  return { optionId, kind, name }
}

// Goose's exact wire shape: four options whose ids, kinds and names are all the
// same snake_case token, emitted allow-first.
const GOOSE_OPTIONS: WirePermissionOption[] = [
  option('allow_always', 'allow_always'),
  option('allow_once', 'allow_once'),
  option('reject_once', 'reject_once'),
  option('reject_always', 'reject_always'),
]

describe('layoutPermissionOptions', () => {
  it('maps the canonical kinds to a negative-first pair plus a Once / Always scope group', () => {
    const layout = layoutPermissionOptions(GOOSE_OPTIONS)

    expect(layout.positive?.optionId).toBe('allow_once')
    expect(layout.negative?.optionId).toBe('reject_once')
    expect(layout.allowScope?.map(o => o.optionId)).toEqual(['allow_once', 'allow_always'])
    // goose is the one agent whose reject_always the scope group also upgrades.
    expect(layout.rememberReject?.optionId).toBe('reject_always')
    // The always options are consumed by the group, not rendered as buttons.
    expect(layout.additional).toEqual([])
  })

  it('keeps a once-only payload answerable with no scope group', () => {
    // Kilo's skill-shell prompts and Copilot's factory tool calls offer no
    // always scope: there is no duration to choose, so Allow is just Allow.
    const layout = layoutPermissionOptions([
      option('once', 'allow_once', 'Allow'),
      option('reject', 'reject_once', 'Reject'),
    ])

    expect(layout.positive?.optionId).toBe('once')
    expect(layout.negative?.optionId).toBe('reject')
    expect(layout.allowScope).toBeUndefined()
    expect(layout.rememberReject).toBeUndefined()
  })

  it('grows the scope group with the agent\'s scopes', () => {
    // OpenCode/Kilo/Cursor/Copilot offer one always scope; no reject_always.
    const layout = layoutPermissionOptions([
      option('once', 'allow_once', 'Allow once'),
      option('always', 'allow_always', 'Always allow'),
      option('reject', 'reject_once', 'Reject'),
    ])

    expect(layout.allowScope?.map(o => o.optionId)).toEqual(['once', 'always'])
    expect(layout.rememberReject).toBeUndefined()
    expect(layout.additional).toEqual([])
  })

  it('turns one allow-once facing two or more always scopes into a wider scope group', () => {
    // Reasonix offers session AND project scopes beside the once option.
    const layout = layoutPermissionOptions([
      option('reasonix_write_once', 'allow_once', 'Allow once'),
      option('reasonix_write_session', 'allow_always', 'Allow these directories for this session'),
      option('reasonix_write_project', 'allow_always', 'Add to project allow_write'),
      option('reasonix_write_deny', 'reject_once', 'Reject'),
    ])

    expect(layout.allowScope?.map(o => o.optionId)).toEqual([
      'reasonix_write_once',
      'reasonix_write_session',
      'reasonix_write_project',
    ])
    expect(layout.positive?.optionId).toBe('reasonix_write_once')
    expect(layout.negative?.optionId).toBe('reasonix_write_deny')
    expect(layout.additional).toEqual([])
  })

  it('keeps an all-allow_once payload out of the scope vocabulary', () => {
    // Cursor routes ask-question answers as plain allow_once options: those are
    // alternative ANSWERS, not durations, so no scope group is drawn and the
    // extras beyond the first stay individual buttons.
    const layout = layoutPermissionOptions([
      option('opt1', 'allow_once', 'First answer'),
      option('opt2', 'allow_once', 'Second answer'),
      option('opt3', 'allow_once', 'Third answer'),
      option('skip', 'reject_once', 'Skip'),
    ])

    expect(layout.allowScope).toBeUndefined()
    expect(layout.positive?.optionId).toBe('opt1')
    expect(layout.additional.map(o => o.optionId)).toEqual(['opt2', 'opt3'])
  })

  it('draws no scope group when the once slot is ambiguous', () => {
    // Two allow_once options face one always: the group cannot know WHICH
    // once answer a "Once" pill means, so the extras stay individual buttons.
    const layout = layoutPermissionOptions([
      option('once_a', 'allow_once', 'Allow once'),
      option('once_b', 'allow_once', 'Allow once more'),
      option('always', 'allow_always', 'Always allow'),
      option('reject', 'reject_once', 'Reject'),
    ])

    expect(layout.allowScope).toBeUndefined()
    expect(layout.positive?.optionId).toBe('once_a')
    expect(layout.additional.map(o => o.optionId)).toEqual(['once_b'])
  })

  it('classifies an empty payload as no buttons', () => {
    expect(layoutPermissionOptions([])).toMatchObject({
      positive: undefined,
      negative: undefined,
      allowScope: undefined,
      additional: [],
    })
  })
})

describe('scopeRemembers', () => {
  it('is false for Once and true for a scope beyond it', () => {
    const layout = layoutPermissionOptions(GOOSE_OPTIONS)

    expect(scopeRemembers(layout)).toBe(false)
    expect(scopeRemembers(layout, 'allow_once')).toBe(false)
    expect(scopeRemembers(layout, 'allow_always')).toBe(true)
    // An id the payload no longer offers clamps back to Once.
    expect(scopeRemembers(layout, 'gone')).toBe(false)
  })

  it('is always false without a scope group', () => {
    const layout = layoutPermissionOptions([
      option('once', 'allow_once', 'Allow'),
      option('reject', 'reject_once', 'Reject'),
    ])
    expect(scopeRemembers(layout, 'once')).toBe(false)
  })
})

describe('resolvePermissionOption', () => {
  it('sends the once options while Once is selected and the always options otherwise', () => {
    const layout = layoutPermissionOptions(GOOSE_OPTIONS)

    expect(resolvePermissionOption(layout, 'allow', false)?.optionId).toBe('allow_once')
    expect(resolvePermissionOption(layout, 'allow', false, 'allow_always')?.optionId).toBe('allow_always')
    expect(resolvePermissionOption(layout, 'reject', false)?.optionId).toBe('reject_once')
    // A remembering scope (scopeRemembers) upgrades Deny for the agents offering reject_always.
    expect(resolvePermissionOption(layout, 'reject', true)?.optionId).toBe('reject_always')
  })

  it('never invents an option the agent did not offer', () => {
    const layout = layoutPermissionOptions([
      option('once', 'allow_once', 'Allow once'),
      option('always', 'allow_always', 'Always allow'),
      option('reject', 'reject_once', 'Reject'),
    ])

    // No reject_always exists, so a remembering scope keeps reject_once -- an
    // unknown optionId is parsed as cancel (goose) or reject (OpenCode), and
    // the user asked to reject, not to cancel.
    expect(resolvePermissionOption(layout, 'reject', true)?.optionId).toBe('reject')
  })

  it('sends the selected scope pill, falling back to the first when none is stored', () => {
    const layout = layoutPermissionOptions([
      option('reasonix_write_once', 'allow_once', 'Allow once'),
      option('reasonix_write_session', 'allow_always', 'Allow these directories for this session'),
      option('reasonix_write_project', 'allow_always', 'Add to project allow_write'),
      option('reasonix_write_deny', 'reject_once', 'Reject'),
    ])

    expect(resolvePermissionOption(layout, 'allow', false)?.optionId).toBe('reasonix_write_once')
    expect(resolvePermissionOption(layout, 'allow', false, 'reasonix_write_project')?.optionId).toBe('reasonix_write_project')
    // A stored id the payload no longer offers clamps to the first scope.
    expect(resolvePermissionOption(layout, 'allow', false, 'gone')?.optionId).toBe('reasonix_write_once')
    expect(resolvePermissionOption(layout, 'reject', true)?.optionId).toBe('reasonix_write_deny')
  })
})

describe('allowScopeLabel', () => {
  it('reads the duration out of the agent\'s own option name', () => {
    expect(allowScopeLabel(option('w_once', 'allow_once', 'Allow once'))).toBe('Once')
    expect(allowScopeLabel(option('w_session', 'allow_always', 'Allow these directories for this session'))).toBe('Session')
    expect(allowScopeLabel(option('w_project', 'allow_always', 'Add to project allow_write'))).toBe('Project')
  })

  it('falls back to Always for a name that names no duration', () => {
    expect(allowScopeLabel(option('always', 'allow_always', 'Always allow'))).toBe('Always')
    // Goose names options after their kind: no duration, still Always.
    expect(allowScopeLabel(option('allow_always', 'allow_always'))).toBe('Always')
  })
})

describe('allowScopePillOptions', () => {
  it('keys the pills by optionId so a selection maps onto the wire reply', () => {
    const scope = [
      option('w_once', 'allow_once', 'Allow once'),
      option('w_session', 'allow_always', 'Allow these directories for this session'),
      option('w_project', 'allow_always', 'Add to project allow_write'),
    ]

    expect(allowScopePillOptions(scope)).toEqual([
      { key: 'w_once', label: 'Once' },
      { key: 'w_session', label: 'Session' },
      { key: 'w_project', label: 'Project' },
    ])
  })

  it('refuses a scope that cannot be a pill group', () => {
    expect(allowScopePillOptions([])).toBeUndefined()
    const six = Array.from({ length: 6 }, (_, i) => option(`w${i}`, i === 0 ? 'allow_once' : 'allow_always'))
    expect(allowScopePillOptions(six)).toBeUndefined()
  })
})

describe('permissionOptionLabel', () => {
  it('shows the agent-provided name when it is a real label', () => {
    expect(permissionOptionLabel(option('once', 'allow_once', 'Allow once'))).toBe('Allow once')
  })

  it('falls back to a friendly label for goose, which names options after their kind', () => {
    expect(permissionOptionLabel(option('allow_once', 'allow_once'))).toBe('Allow once')
    expect(permissionOptionLabel(option('allow_always', 'allow_always'))).toBe('Allow always')
    expect(permissionOptionLabel(option('reject_once', 'reject_once'))).toBe('Reject')
    expect(permissionOptionLabel(option('reject_always', 'reject_always'))).toBe('Reject always')
  })
})

describe('isRejectPermissionKind', () => {
  it('covers both reject kinds', () => {
    expect(isRejectPermissionKind('reject_once')).toBe(true)
    expect(isRejectPermissionKind('reject_always')).toBe(true)
    expect(isRejectPermissionKind('allow_once')).toBe(false)
    expect(isRejectPermissionKind('allow_always')).toBe(false)
  })
})
