import type { MessageBandKind } from './chatRowGeometry'
import type { MessageCategory } from './messageClassification'
import { describe, expect, it, vi } from 'vitest'
import { kindScopedLayoutKey, messageBandKind } from './chatRowGeometry'

/**
 * The band each message kind paints. The `Record<MessageCategory['kind'], ...>`
 * annotation IS the guard: both functions under test take a bare `string` -- the
 * virtualizer hands them `item.kind ?? ''` -- so nothing in the source forces a
 * NEW kind to be considered. Here it becomes a `tsc` failure, and the author has
 * to say whether the new kind paints a band before the suite compiles.
 *
 * The tables live in the test rather than in `./chatRowGeometry`, on purpose.
 * That module is imported by `./messageStyles.css.ts`, so it sits on the
 * vanilla-extract build-evaluation path, and its doc states the import-free
 * property as the reason it exists. A type-only import is erased today, but it
 * would falsify that stated property and leave one slip between the stylesheet
 * build and the provider registry. The type checker reports the same omission
 * from here, in the same CI run, at no risk to that graph.
 */
const BAND_BY_KIND: Record<MessageCategory['kind'], MessageBandKind | undefined> = {
  assistant_text: 'text',
  assistant_thinking: 'thought',
  // plan_execution shares the thought expander but stays a right-aligned accent
  // bubble, so it must NOT join the band set -- it would also lose its gap
  // against the assistant rows around it.
  plan_execution: undefined,
  agent_prompt: undefined,
  compact_summary: undefined,
  control_response: undefined,
  hidden: undefined,
  notification: undefined,
  result_divider: undefined,
  tool_result: undefined,
  tool_use: undefined,
  unknown: undefined,
  unsupported_provider: undefined,
  user_content: undefined,
  user_text: undefined,
}

/** The scoped-layout term each kind contributes, under diffView 'split' + expanded thoughts. */
const SCOPED_KEY_BY_KIND: Record<MessageCategory['kind'], string> = {
  tool_use: '|d:split',
  tool_result: '|d:split',
  assistant_thinking: '|t:1',
  agent_prompt: '',
  assistant_text: '',
  compact_summary: '',
  control_response: '',
  hidden: '',
  notification: '',
  plan_execution: '',
  result_divider: '',
  unknown: '',
  unsupported_provider: '',
  user_content: '',
  user_text: '',
}

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

    it('adds a scoped term for exactly the kinds a scoped pref can resize', () => {
      for (const [kind, expected] of Object.entries(SCOPED_KEY_BY_KIND))
        expect(kindScopedLayoutKey(kind, () => 'split', () => true)).toBe(expected)
    })

    it('adds nothing for a kind no scoped pref can resize', () => {
      // A change in the omitted term must leave these keys byte-identical, so a global
      // diffView / expandThoughts toggle never re-measures them.
      for (const [kind, expected] of Object.entries(SCOPED_KEY_BY_KIND)) {
        if (expected !== '')
          continue
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

  describe('messageBandKind', () => {
    it('maps an assistant message to the solid-line band', () => {
      expect(messageBandKind('assistant_text')).toBe('text')
    })

    it('maps an assistant thought to the dashed-line band', () => {
      expect(messageBandKind('assistant_thinking')).toBe('thought')
    })

    it('decides the band for every kind the classifier can produce', () => {
      for (const [kind, band] of Object.entries(BAND_BY_KIND))
        expect(messageBandKind(kind)).toBe(band)
    })

    it('treats an unclassified or a packed tool kind as no band', () => {
      // The virtualizer passes `item.kind ?? ''`, and a tool row's kind reaches other
      // callers packed as `tool_use:<name>`. Neither is a member of the union above.
      expect(messageBandKind('')).toBeUndefined()
      expect(messageBandKind('tool_use:Bash')).toBeUndefined()
    })
  })
})
