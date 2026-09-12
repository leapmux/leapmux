import { describe, expect, it } from 'vitest'
import { unescapeAutolinkDots } from './autolinkDotEscape'

describe('unescapeAutolinkDots', () => {
  it('undoes the escape the autolink rule adds to an ordinary filename', () => {
    expect(unescapeAutolinkDots('Create blank-new\\.txt here')).toBe('Create blank-new.txt here')
  })

  it('undoes it for every word that happens to end in w', () => {
    expect(unescapeAutolinkDots('raw\\.json show\\.me draw\\.io flow\\.py NEW\\.TXT'))
      .toBe('raw.json show.me draw.io flow.py NEW.TXT')
  })

  it('leaves a dot that carries no escape', () => {
    expect(unescapeAutolinkDots('file.txt and newt.txt')).toBe('file.txt and newt.txt')
  })

  it('leaves an escape the rule never adds, so no other escape is disturbed', () => {
    expect(unescapeAutolinkDots('2 \\* 3 and a\\_b and x\\&y')).toBe('2 \\* 3 and a\\_b and x\\&y')
  })

  // The document holds what the reader typed, so a literal backslash serializes as two
  // characters. Only a single one is the autolink rule's.
  it('keeps a backslash the reader typed', () => {
    expect(unescapeAutolinkDots('new\\\\.txt')).toBe('new\\\\.txt')
  })

  // A dot at the end of a sentence is not escaped by the rule (its `after` requires a
  // word character), so nothing here should change either way.
  it('leaves a sentence ending in w followed by a full stop', () => {
    expect(unescapeAutolinkDots('I saw the file now\\. Then I left.')).toBe('I saw the file now\\. Then I left.')
  })

  describe('code is verbatim', () => {
    it('leaves an inline code span alone', () => {
      expect(unescapeAutolinkDots('outside new\\.txt `inside new\\.txt` outside new\\.txt'))
        .toBe('outside new.txt `inside new\\.txt` outside new.txt')
    })

    it('leaves a fenced block alone', () => {
      const input = 'before new\\.txt\n```\nnew\\.txt\n```\nafter new\\.txt'
      expect(unescapeAutolinkDots(input)).toBe('before new.txt\n```\nnew\\.txt\n```\nafter new.txt')
    })

    it('leaves a tilde fence alone', () => {
      const input = '~~~\nnew\\.txt\n~~~\nafter new\\.txt'
      expect(unescapeAutolinkDots(input)).toBe('~~~\nnew\\.txt\n~~~\nafter new.txt')
    })

    it('does not let a tilde close a backtick fence', () => {
      const input = '```\nnew\\.txt\n~~~\nstill new\\.txt\n```\nout new\\.txt'
      expect(unescapeAutolinkDots(input)).toBe('```\nnew\\.txt\n~~~\nstill new\\.txt\n```\nout new.txt')
    })

    it('handles a double-backtick span holding a backtick', () => {
      expect(unescapeAutolinkDots('a new\\.txt ``x ` new\\.txt`` b new\\.txt'))
        .toBe('a new.txt ``x ` new\\.txt`` b new.txt')
    })

    it('treats an unclosed run as opening nothing that ends', () => {
      expect(unescapeAutolinkDots('a new\\.txt ` b new\\.txt')).toBe('a new.txt ` b new\\.txt')
    })

    it('leaves a fence that the document never closes', () => {
      expect(unescapeAutolinkDots('before new\\.txt\n```\nnew\\.txt')).toBe('before new.txt\n```\nnew\\.txt')
    })

    it('keeps an indented fence recognizable', () => {
      const input = '   ```\nnew\\.txt\n   ```\nafter new\\.txt'
      expect(unescapeAutolinkDots(input)).toBe('   ```\nnew\\.txt\n   ```\nafter new.txt')
    })
  })

  it('leaves empty input alone', () => {
    expect(unescapeAutolinkDots('')).toBe('')
  })
})
