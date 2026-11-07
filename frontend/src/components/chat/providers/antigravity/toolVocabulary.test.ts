import type { ToolVocabularyCheck } from '~/test-support/toolVocabulary'
import { describe, expect, it } from 'vitest'
import { ANTIGRAVITY_TOOL } from '~/generated/contracts/antigravity-protocol'
import { documentedNamesThatReachAKind, staleGenericNames, undocumentedFallbacks } from '~/test-support/toolVocabulary'
import { ANTIGRAVITY_TOOL_KINDS, antigravityToolKind } from './toolKinds'
import { ANTIGRAVITY_GENERIC_TOOLS } from './toolResults.fixtures'

const CHECK: ToolVocabularyCheck = {
  names: Object.values(ANTIGRAVITY_TOOL),
  kindOf: antigravityToolKind,
  generic: ANTIGRAVITY_GENERIC_TOOLS,
  fallback: 'other',
}

describe('the Antigravity CLI tool vocabulary', () => {
  it('covers every tool the contract lists', () => {
    expect(undocumentedFallbacks(CHECK)).toEqual([])
  })

  it('keeps every documented fallback pinned to a tool that exists', () => {
    expect(staleGenericNames(CHECK)).toEqual([])
  })

  it('documents no fallback for a tool that reaches a kind', () => {
    expect(documentedNamesThatReachAKind(CHECK)).toEqual([])
  })

  it('uses a Map for hostile and unknown names', () => {
    expect(antigravityToolKind('__proto__')).toBe('other')
    expect(antigravityToolKind('constructor')).toBe('other')
    expect(antigravityToolKind('')).toBe('unspecified')
    expect(ANTIGRAVITY_TOOL_KINDS.get(ANTIGRAVITY_TOOL.ViewFile)).toBe('read')
  })
})
