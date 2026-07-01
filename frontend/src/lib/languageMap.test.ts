import { describe, expect, it } from 'vitest'
import { EXT_TO_LANG, guessLanguage } from './languageMap'
import { resolveBundledLang } from './shikiLazyHighlighter'

describe('guessLanguage', () => {
  it('maps common source extensions to Shiki language ids', () => {
    const cases: [string, string][] = [
      ['foo.ts', 'typescript'],
      ['foo.tsx', 'tsx'],
      ['a/b.rs', 'rust'],
      ['x.rb', 'ruby'],
      ['x.php', 'php'],
      ['x.swift', 'swift'],
      ['x.kt', 'kotlin'],
      ['x.scala', 'scala'],
      ['x.cs', 'csharp'],
      ['x.go', 'go'],
      ['x.py', 'python'],
      ['x.lua', 'lua'],
      ['x.r', 'r'],
      ['x.jl', 'julia'],
      ['x.hs', 'haskell'],
      ['x.ex', 'elixir'],
      ['x.clj', 'clojure'],
      ['x.ml', 'ocaml'],
      ['x.vue', 'vue'],
      ['x.svelte', 'svelte'],
      ['x.graphql', 'graphql'],
      ['x.scss', 'scss'],
      ['x.tf', 'terraform'],
      ['x.proto', 'proto'],
      ['x.sol', 'solidity'],
      ['x.ps1', 'powershell'],
      ['x.zig', 'zig'],
      ['x.dart', 'dart'],
      ['x.mm', 'objective-cpp'],
    ]
    for (const [path, lang] of cases)
      expect(guessLanguage(path), path).toBe(lang)
  })

  it('lowercases the extension before lookup', () => {
    expect(guessLanguage('README.MD')).toBe('markdown')
    expect(guessLanguage('Foo.TS')).toBe('typescript')
  })

  it('keeps .log mapped to the ansi built-in', () => {
    expect(guessLanguage('build.log')).toBe('ansi')
  })

  it('returns undefined for unknown or extensionless paths', () => {
    expect(guessLanguage('file.unknownext')).toBeUndefined()
    expect(guessLanguage('Makefile')).toBeUndefined()
    expect(guessLanguage('noext')).toBeUndefined()
  })

  it('does not return Object.prototype values for prototype-key extensions', () => {
    // EXT_TO_LANG is a plain record; an unguarded `EXT_TO_LANG[ext]` returns the inherited
    // `Object` constructor / prototype for a file like `x.constructor`, yielding a non-string
    // "language" that later fails structured-clone in the worker. Must be undefined.
    expect(guessLanguage('x.constructor')).toBeUndefined()
    expect(guessLanguage('x.toString')).toBeUndefined()
    expect(guessLanguage('weird.hasOwnProperty')).toBeUndefined()
  })

  it('maps EVERY extension (except the ansi built-in) to a real Shiki bundled grammar', () => {
    // Exhaustive, not sampled: iterate the actual map so a typo in ANY value (e.g.
    // `csharp` vs `c-sharp`, `objective-cpp` vs `objectivecpp`) is caught. A bad id
    // silently renders a recognized extension as plain text, so this is the guard.
    for (const [ext, lang] of Object.entries(EXT_TO_LANG)) {
      if (lang === 'ansi') {
        // `ansi` is a Shiki built-in tokenized on the main thread (renderAnsi /
        // ReadResultView), NOT a bundled grammar -- it must NOT resolve.
        expect(resolveBundledLang(lang), `${ext} -> ${lang}`).toBeUndefined()
        continue
      }
      expect(resolveBundledLang(lang), `${ext} -> ${lang}`).toBeDefined()
    }
  })

  it('keeps guessLanguage and EXT_TO_LANG in agreement for every entry', () => {
    // The public guessLanguage path must return exactly the mapped id for every
    // registered extension (Object.hasOwn guard + lowercase lookup), so the
    // exhaustive resolution check above actually covers what users hit.
    for (const [ext, lang] of Object.entries(EXT_TO_LANG))
      expect(guessLanguage(`file.${ext}`), ext).toBe(lang)
  })
})
