import { afterEach, describe, expect, it } from 'vitest'

import { deleteContext, evaluateWhen, getContext, registerLazyContext, resetContext, resetParseCache, setContext, unregisterLazyContext } from './context'

afterEach(() => {
  resetContext()
  resetParseCache()
})

describe('context store', () => {
  it('sets and gets a value', () => {
    setContext('foo', true)
    expect(getContext('foo')).toBe(true)
  })

  it('returns undefined for unset keys', () => {
    expect(getContext('missing')).toBeUndefined()
  })

  it('deletes a key', () => {
    setContext('foo', 'bar')
    deleteContext('foo')
    expect(getContext('foo')).toBeUndefined()
  })

  it('overwrites existing values', () => {
    setContext('foo', 'a')
    setContext('foo', 'b')
    expect(getContext('foo')).toBe('b')
  })

  it('lazy provider is called on getContext', () => {
    let count = 0
    registerLazyContext('lazy', () => {
      count++
      return true
    })
    expect(getContext('lazy')).toBe(true)
    expect(count).toBe(1)
    expect(getContext('lazy')).toBe(true)
    expect(count).toBe(2)
    unregisterLazyContext('lazy')
    expect(getContext('lazy')).toBeUndefined()
  })
})

describe('evaluateWhen', () => {
  it('returns true for empty/undefined expressions', () => {
    expect(evaluateWhen(undefined)).toBe(true)
    expect(evaluateWhen('')).toBe(true)
    expect(evaluateWhen('  ')).toBe(true)
  })

  describe('bare identifiers (truthy check)', () => {
    it('returns true when context value is truthy', () => {
      setContext('dialogOpen', true)
      expect(evaluateWhen('dialogOpen')).toBe(true)
    })

    it('returns false when context value is false', () => {
      setContext('dialogOpen', false)
      expect(evaluateWhen('dialogOpen')).toBe(false)
    })

    it('returns false for undefined keys', () => {
      expect(evaluateWhen('unknownKey')).toBe(false)
    })

    it('returns true for truthy strings', () => {
      setContext('mode', 'active')
      expect(evaluateWhen('mode')).toBe(true)
    })

    it('returns false for empty strings', () => {
      setContext('mode', '')
      expect(evaluateWhen('mode')).toBe(false)
    })

    it('returns true for non-zero numbers', () => {
      setContext('count', 5)
      expect(evaluateWhen('count')).toBe(true)
    })

    it('returns false for zero', () => {
      setContext('count', 0)
      expect(evaluateWhen('count')).toBe(false)
    })
  })

  describe('negation', () => {
    it('negates truthy value', () => {
      setContext('inputFocused', true)
      expect(evaluateWhen('!inputFocused')).toBe(false)
    })

    it('negates falsy value', () => {
      setContext('inputFocused', false)
      expect(evaluateWhen('!inputFocused')).toBe(true)
    })

    it('negates undefined key', () => {
      expect(evaluateWhen('!unknownKey')).toBe(true)
    })

    it('double negation', () => {
      setContext('a', true)
      expect(evaluateWhen('!!a')).toBe(true)
    })
  })

  describe('aND (&&)', () => {
    it('true && true = true', () => {
      setContext('a', true)
      setContext('b', true)
      expect(evaluateWhen('a && b')).toBe(true)
    })

    it('true && false = false', () => {
      setContext('a', true)
      setContext('b', false)
      expect(evaluateWhen('a && b')).toBe(false)
    })

    it('false && true = false', () => {
      setContext('a', false)
      setContext('b', true)
      expect(evaluateWhen('a && b')).toBe(false)
    })

    it('false && false = false', () => {
      setContext('a', false)
      setContext('b', false)
      expect(evaluateWhen('a && b')).toBe(false)
    })

    it('chained: a && b && c', () => {
      setContext('a', true)
      setContext('b', true)
      setContext('c', true)
      expect(evaluateWhen('a && b && c')).toBe(true)
      setContext('b', false)
      expect(evaluateWhen('a && b && c')).toBe(false)
    })
  })

  describe('oR (||)', () => {
    it('true || false = true', () => {
      setContext('a', true)
      setContext('b', false)
      expect(evaluateWhen('a || b')).toBe(true)
    })

    it('false || true = true', () => {
      setContext('a', false)
      setContext('b', true)
      expect(evaluateWhen('a || b')).toBe(true)
    })

    it('false || false = false', () => {
      setContext('a', false)
      setContext('b', false)
      expect(evaluateWhen('a || b')).toBe(false)
    })
  })

  describe('equality (==)', () => {
    it('matches string values', () => {
      setContext('platform', 'mac')
      expect(evaluateWhen('platform == "mac"')).toBe(true)
      expect(evaluateWhen('platform == "linux"')).toBe(false)
    })

    it('handles single-quoted strings', () => {
      setContext('platform', 'mac')
      expect(evaluateWhen('platform == \'mac\'')).toBe(true)
    })

    it('handles unquoted values as identifiers', () => {
      setContext('activeTabType', 'terminal')
      expect(evaluateWhen('activeTabType == terminal')).toBe(true)
    })

    it('undefined key compared to empty string', () => {
      expect(evaluateWhen('missing == ""')).toBe(true)
    })
  })

  describe('not-equal (!=)', () => {
    it('returns true when values differ', () => {
      setContext('activeTabType', 'agent')
      expect(evaluateWhen('activeTabType != "terminal"')).toBe(true)
    })

    it('returns false when values match', () => {
      setContext('activeTabType', 'terminal')
      expect(evaluateWhen('activeTabType != "terminal"')).toBe(false)
    })
  })

  describe('parentheses', () => {
    it('groups OR inside AND', () => {
      setContext('a', false)
      setContext('b', true)
      setContext('c', true)
      // Without parens: a || (b && c) = true (AND binds tighter)
      expect(evaluateWhen('a || b && c')).toBe(true)
      // With parens: (a || b) && c = true
      expect(evaluateWhen('(a || b) && c')).toBe(true)
    })

    it('changes evaluation order', () => {
      setContext('a', true)
      setContext('b', false)
      setContext('c', false)
      // a && b || c = false || false = false
      expect(evaluateWhen('a && b || c')).toBe(false)
      // a && (b || c) = true && false = false
      expect(evaluateWhen('a && (b || c)')).toBe(false)
      // Now test where parens matter
      setContext('c', true)
      // a && b || c = false || true = true
      expect(evaluateWhen('a && b || c')).toBe(true)
      // a && (b || c) = true && true = true
      expect(evaluateWhen('a && (b || c)')).toBe(true)
    })
  })

  describe('complex expressions', () => {
    it('editorFocused && !dialogOpen', () => {
      setContext('editorFocused', true)
      setContext('dialogOpen', false)
      expect(evaluateWhen('editorFocused && !dialogOpen')).toBe(true)

      setContext('dialogOpen', true)
      expect(evaluateWhen('editorFocused && !dialogOpen')).toBe(false)
    })

    it('isDesktop && (platform == "mac" || platform == "linux")', () => {
      setContext('isDesktop', true)
      setContext('platform', 'mac')
      expect(evaluateWhen('isDesktop && (platform == "mac" || platform == "linux")')).toBe(true)

      setContext('platform', 'windows')
      expect(evaluateWhen('isDesktop && (platform == "mac" || platform == "linux")')).toBe(false)

      setContext('isDesktop', false)
      setContext('platform', 'mac')
      expect(evaluateWhen('isDesktop && (platform == "mac" || platform == "linux")')).toBe(false)
    })

    it('!inputFocused && activeTabType == "agent"', () => {
      setContext('inputFocused', false)
      setContext('activeTabType', 'agent')
      expect(evaluateWhen('!inputFocused && activeTabType == "agent"')).toBe(true)
    })
  })

  describe('boolean literals', () => {
    it('true literal', () => {
      expect(evaluateWhen('true')).toBe(true)
    })

    it('false literal', () => {
      expect(evaluateWhen('false')).toBe(false)
    })
  })

  describe('custom context getter', () => {
    it('uses provided getter instead of global context', () => {
      setContext('x', false) // global says false
      const getter = (key: string) => (key === 'x' ? true : undefined)
      expect(evaluateWhen('x', getter)).toBe(true)
    })
  })
})
