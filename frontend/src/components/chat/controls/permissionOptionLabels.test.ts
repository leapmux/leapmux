import type { PermissionOption } from '~/components/chat/model/controlPrompt'
import { describe, expect, it } from 'vitest'
import { isAllowPermissionKind, isRejectPermissionKind } from '~/components/chat/model/controlPrompt'
import { permissionOptionLabel } from './permissionOptionLabels'

function option(optionId: string, kind: string, name = optionId): PermissionOption {
  return { optionId, kind, name }
}

describe('permissionOptionLabel', () => {
  it('shows the agent-provided name when it is a real label', () => {
    expect(permissionOptionLabel(option('once', 'allow_once', 'Allow once'))).toBe('Allow once')
  })

  it('falls back to a friendly label for goose, which sets each option\'s name to its kind', () => {
    expect(permissionOptionLabel(option('allow_once', 'allow_once'))).toBe('Allow once')
    expect(permissionOptionLabel(option('allow_always', 'allow_always'))).toBe('Allow always')
    expect(permissionOptionLabel(option('reject_once', 'reject_once'))).toBe('Reject')
    expect(permissionOptionLabel(option('reject_always', 'reject_always'))).toBe('Reject always')
  })

  it('falls back to the id when an option has neither name nor known kind', () => {
    expect(permissionOptionLabel({ optionId: 'opt1', kind: 'answer' })).toBe('opt1')
  })

  // `kind` comes straight off the wire. A bare index answered `Object.prototype`
  // here -- a truthy value, so the two fallbacks below it never ran and the
  // function's SOURCE TEXT became the decision button's label.
  it.each(['toString', 'constructor', 'valueOf', 'hasOwnProperty', '__proto__'])(
    'falls back for a kind spelled %s, which names an Object.prototype member',
    (kind) => {
      expect(permissionOptionLabel({ optionId: 'opt1', kind })).toBe('opt1')
      expect(permissionOptionLabel({ optionId: 'opt1', kind, name: 'Run it' })).toBe('Run it')
    },
  )
})

describe('isRejectPermissionKind', () => {
  it('covers both reject kinds', () => {
    expect(isRejectPermissionKind('reject_once')).toBe(true)
    expect(isRejectPermissionKind('reject_always')).toBe(true)
    expect(isRejectPermissionKind('allow_once')).toBe(false)
    expect(isRejectPermissionKind('allow_always')).toBe(false)
  })
})

describe('isAllowPermissionKind', () => {
  it('covers both allow kinds and nothing else', () => {
    expect(isAllowPermissionKind('allow_once')).toBe(true)
    expect(isAllowPermissionKind('allow_always')).toBe(true)
    expect(isAllowPermissionKind('reject_once')).toBe(false)
    expect(isAllowPermissionKind('reject_always')).toBe(false)
    expect(isAllowPermissionKind('answer')).toBe(false)
  })
})
