/// <reference types="vitest/globals" />
import type { TitleState } from '~/hooks/createTitleState'
import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { NAME_BYTE_LIMIT } from '~/generated/contracts/validate'
import { createTitleState } from '~/hooks/createTitleState'

/**
 * Runs `body` with a state whose reactive root stays alive for the whole call.
 *
 * The root cannot be disposed before the assertions: `cleaned` and `error`
 * read a memo, and a disposed memo stops recomputing while still answering
 * with its last value — so a test that disposed first would assert against
 * the INITIAL title no matter what it set, and pass for the wrong reason.
 */
function withTitleState(generate: () => string, body: (state: TitleState) => void): void {
  createRoot((dispose) => {
    body(createTitleState(generate))
    dispose()
  })
}

describe('createTitleState', () => {
  it('seeds the value from the generator', () => {
    withTitleState(() => 'Agent Gabe', (state) => {
      expect(state.value()).toBe('Agent Gabe')
      expect(state.error()).toBeNull()
    })
  })

  it('calls the generator once for the initial value', () => {
    let calls = 0
    withTitleState(() => `Agent ${++calls}`, () => {})
    expect(calls).toBe(1)
  })

  it('replaces the value on regenerate', () => {
    let n = 0
    withTitleState(() => `Agent ${++n}`, (state) => {
      expect(state.value()).toBe('Agent 1')
      state.regenerate()
      expect(state.value()).toBe('Agent 2')
    })
  })

  // regenerate must overwrite what the user typed — that is the whole point of
  // the button. The risk is the opposite bug: a guard that skipped the write
  // once the field was dirty.
  it('overwrites a value the user typed', () => {
    withTitleState(() => 'Agent Gabe', (state) => {
      state.setValue('my own name')
      state.regenerate()
      expect(state.value()).toBe('Agent Gabe')
    })
  })

  // regenerateIfPristine exists for a generator that reads another control
  // (ChangeBranchDialog's Agent/Terminal toggle): the prefix has to follow
  // that control, but not at the cost of a name the user typed.
  describe('regenerateIfPristine', () => {
    it('re-rolls while the value is still the generated one', () => {
      let n = 0
      withTitleState(() => `Agent ${++n}`, (state) => {
        expect(state.value()).toBe('Agent 1')
        state.regenerateIfPristine()
        expect(state.value()).toBe('Agent 2')
      })
    })

    it('leaves a value the user typed alone', () => {
      let n = 0
      withTitleState(() => `Agent ${++n}`, (state) => {
        state.setValue('my own name')
        state.regenerateIfPristine()
        expect(state.value()).toBe('my own name')
      })
    })

    // Clicking the refresh button leaves the field generator-owned, so a
    // later toggle flip may still re-roll it. Treating regenerate as "dirty"
    // would strand the prefix after one click of refresh.
    it('still re-rolls after an explicit regenerate', () => {
      let n = 0
      withTitleState(() => `Agent ${++n}`, (state) => {
        state.regenerate()
        expect(state.value()).toBe('Agent 2')
        state.regenerateIfPristine()
        expect(state.value()).toBe('Agent 3')
      })
    })

    // Typing the generated string back by hand is indistinguishable from
    // never having touched it, and that is the intended reading: the field
    // holds exactly what the generator would produce, so re-rolling is safe.
    it('treats a value typed back to the generated one as pristine', () => {
      let n = 0
      withTitleState(() => `Agent ${++n}`, (state) => {
        state.setValue('something else')
        state.setValue('Agent 1')
        state.regenerateIfPristine()
        expect(state.value()).toBe('Agent 2')
      })
    })

    it('does not re-roll a value the user cleared', () => {
      let n = 0
      withTitleState(() => `Agent ${++n}`, (state) => {
        state.setValue('')
        state.regenerateIfPristine()
        expect(state.value()).toBe('')
        expect(state.error()).toBe('Name must not be empty')
      })
    })
  })

  it('reports an empty value as an error', () => {
    withTitleState(() => 'Agent Gabe', (state) => {
      state.setValue('')
      expect(state.error()).toBe('Name must not be empty')
    })
  })

  // Whitespace, a control character and an invisible format character all
  // CLEAN to nothing, so each must report the empty error rather than send a
  // blank title the server would silently re-name.
  //
  // The last two are ESCAPES, not the characters themselves:
  // `noControlBytesInSource` fails the suite on a literal control byte in a
  // source file, and a literal zero-width space is invisible to the reader.
  it.each([
    ['whitespace only', '   '],
    ['a control character alone', '\u0000'],
    ['an invisible format character alone', '\u200B'],
  ])('reports %s as empty', (_label, typed) => {
    withTitleState(() => 'Agent Gabe', (state) => {
      state.setValue(typed)
      expect(state.error()).toBe('Name must not be empty')
    })
  })

  it('reports a value over the byte limit', () => {
    withTitleState(() => 'Agent Gabe', (state) => {
      state.setValue('a'.repeat(NAME_BYTE_LIMIT + 1))
      expect(state.error()).toBe(`Name must be at most ${NAME_BYTE_LIMIT} bytes`)
    })
  })

  // The limit counts UTF-8 BYTES, because the server's `len` does. A count of
  // characters accepts this and the server then refuses it.
  it('counts a multi-byte character by its UTF-8 size', () => {
    withTitleState(() => 'Agent Gabe', (state) => {
      // 3 bytes each: 43 of them is 129 bytes, one over the 128-byte limit.
      state.setValue('한'.repeat(43))
      expect(state.error()).toBe(`Name must be at most ${NAME_BYTE_LIMIT} bytes`)
      state.setValue('한'.repeat(42))
      expect(state.error()).toBeNull()
    })
  })

  it('exposes the cleaned value while the input keeps what was typed', () => {
    withTitleState(() => 'Agent Gabe', (state) => {
      state.setValue('  Auth  fix now  ')
      expect(state.cleaned()).toBe('Auth fix now')
      expect(state.value()).toBe('  Auth  fix now  ')
    })
  })

  // The clean must not become a second, stricter character ban on this side:
  // the punctuation the rule KEEPS has to reach the wire untouched.
  it('leaves visible punctuation alone', () => {
    withTitleState(() => 'Agent Gabe', (state) => {
      state.setValue('100% of $HOME "quoted"')
      expect(state.cleaned()).toBe('100% of $HOME "quoted"')
      expect(state.error()).toBeNull()
    })
  })
})
