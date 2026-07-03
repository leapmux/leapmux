import { describe, expect, it, vi } from 'vitest'
import { kindScopedLayoutKey } from './chatRowGeometry'

describe('chatrowgeometry', () => {
  describe('kindscopedlayoutkey', () => {
    it('folds the effective diff-view value into tool_use and tool_result rows', () => {
      expect(kindScopedLayoutKey('tool_use', () => 'split', () => true)).toBe('|d:split')
      expect(kindScopedLayoutKey('tool_result', () => 'unified', () => true)).toBe('|d:unified')
    })

    it('folds the effective thinking-expand state into assistant_thinking rows', () => {
      expect(kindScopedLayoutKey('assistant_thinking', () => 'split', () => true)).toBe('|t:1')
      expect(kindScopedLayoutKey('assistant_thinking', () => 'split', () => false)).toBe('|t:0')
    })

    it('adds nothing for a kind no scoped pref can resize', () => {
      // A change in the omitted term must leave these keys byte-identical, so a global
      // diffView / expandThoughts toggle never re-measures them.
      for (const kind of ['assistant_text', 'user_text', 'user_content', 'plan_execution', 'agent_prompt', 'notification', 'unknown']) {
        expect(kindScopedLayoutKey(kind, () => 'split', () => true)).toBe('')
        expect(kindScopedLayoutKey(kind, () => 'unified', () => false)).toBe('')
      }
    })

    it('resolves ONLY the dimension its kind depends on (the other resolver is never called)', () => {
      const diff = vi.fn(() => 'split')
      const think = vi.fn(() => true)

      kindScopedLayoutKey('tool_use', diff, think)
      expect(diff).toHaveBeenCalledTimes(1)
      expect(think).not.toHaveBeenCalled()

      diff.mockClear()
      think.mockClear()
      kindScopedLayoutKey('assistant_thinking', diff, think)
      expect(think).toHaveBeenCalledTimes(1)
      expect(diff).not.toHaveBeenCalled()

      diff.mockClear()
      think.mockClear()
      kindScopedLayoutKey('user_text', diff, think)
      expect(diff).not.toHaveBeenCalled()
      expect(think).not.toHaveBeenCalled()
    })
  })
})
