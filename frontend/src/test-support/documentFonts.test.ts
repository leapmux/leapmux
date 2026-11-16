import { beforeEach, describe, expect, it, onTestFinished } from 'vitest'
import { stubDocumentFonts } from './documentFonts'

/** The `document.fonts` property, which the DOM type declares but jsdom does not implement. */
function currentFonts(): unknown {
  return (document as { fonts?: unknown }).fonts
}

/**
 * Check when the running test finishes that `document.fonts` is `expected`, and that the
 * property is absent when `expected` is `undefined`. Then remove the property, so a failed
 * check leaves no stub for the next test.
 *
 * Vitest runs the callbacks of `onTestFinished` in reverse order, so call this BEFORE
 * stubDocumentFonts: its check then runs after the restore of the helper.
 */
function expectFontsWhenTestFinishes(expected: unknown): void {
  onTestFinished(() => {
    try {
      expect(currentFonts()).toBe(expected)
      expect('fonts' in document).toBe(expected !== undefined)
    }
    finally {
      Reflect.deleteProperty(document, 'fonts')
    }
  })
}

describe('stubDocumentFonts', () => {
  beforeEach(() => {
    // jsdom has no `document.fonts`, so a test starts without one.
    Reflect.deleteProperty(document, 'fonts')
  })

  it('defines the given API on the document', () => {
    const api = {
      check: () => true,
      load: () => Promise.resolve(),
    }
    expectFontsWhenTestFinishes(undefined)

    stubDocumentFonts(api)

    expect(currentFonts()).toBe(api)
  })

  it('removes the property of the document for undefined, and restores it when the test finishes', () => {
    const before = {
      check: () => true,
    }
    Object.defineProperty(document, 'fonts', { configurable: true, value: before })
    expectFontsWhenTestFinishes(before)

    stubDocumentFonts(undefined)

    expect('fonts' in document).toBe(false)
    expect(currentFonts()).toBeUndefined()
  })

  it('restores the API that the document had before, when the test finishes', () => {
    const before = {
      check: () => true,
    }
    Object.defineProperty(document, 'fonts', { configurable: true, value: before })
    expectFontsWhenTestFinishes(before)

    stubDocumentFonts({
      check: () => false,
    })

    expect(currentFonts()).not.toBe(before)
  })

  it('removes the property again when the document had none before, when the test finishes', () => {
    expectFontsWhenTestFinishes(undefined)

    stubDocumentFonts({
      check: () => false,
    })

    expect('fonts' in document).toBe(true)
  })

  it('restores the original property when a test stubs twice', () => {
    const before = {
      check: () => true,
    }
    Object.defineProperty(document, 'fonts', { configurable: true, value: before })
    expectFontsWhenTestFinishes(before)
    const first = {
      check: () => false,
    }
    const second = {
      load: () => Promise.resolve(),
    }

    stubDocumentFonts(first)
    expect(currentFonts()).toBe(first)
    stubDocumentFonts(second)

    expect(currentFonts()).toBe(second)
  })
})
