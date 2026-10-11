import { describe, expect, it } from 'vitest'
import { stripNeedlessEscapes } from './needlessEscapes'

describe('stripNeedlessEscapes', () => {
  it('undoes the escape the autolink rule adds to an ordinary filename', () => {
    expect(stripNeedlessEscapes('Create blank-new\\.txt here')).toBe('Create blank-new.txt here')
  })

  it('undoes it for every word that happens to end in w', () => {
    expect(stripNeedlessEscapes('raw\\.json show\\.me draw\\.io flow\\.py NEW\\.TXT'))
      .toBe('raw.json show.me draw.io flow.py NEW.TXT')
  })

  it('leaves a dot that carries no escape', () => {
    expect(stripNeedlessEscapes('file.txt and newt.txt')).toBe('file.txt and newt.txt')
  })

  // `\_a` stands for an underscore that can still open emphasis. The underscore inside
  // a word has its own cases below.
  it('leaves an escape that is not needless, so no other escape is disturbed', () => {
    expect(stripNeedlessEscapes('2 \\* 3 and \\_a and x\\&y')).toBe('2 \\* 3 and \\_a and x\\&y')
  })

  // The document holds what the reader typed, so a literal backslash serializes as two
  // characters. Only a single one is the autolink rule's.
  it('keeps a backslash the reader typed', () => {
    expect(stripNeedlessEscapes('new\\\\.txt')).toBe('new\\\\.txt')
  })

  // A dot at the end of a sentence is not escaped by the rule (its `after` requires a
  // word character), so nothing here should change either way.
  it('leaves a sentence ending in w followed by a full stop', () => {
    expect(stripNeedlessEscapes('I saw the file now\\. Then I left.')).toBe('I saw the file now\\. Then I left.')
  })

  describe('code is verbatim', () => {
    it('leaves an inline code span alone', () => {
      expect(stripNeedlessEscapes('outside new\\.txt `inside new\\.txt` outside new\\.txt'))
        .toBe('outside new.txt `inside new\\.txt` outside new.txt')
    })

    it('leaves a fenced block alone', () => {
      const input = 'before new\\.txt\n```\nnew\\.txt\n```\nafter new\\.txt'
      expect(stripNeedlessEscapes(input)).toBe('before new.txt\n```\nnew\\.txt\n```\nafter new.txt')
    })

    it('leaves a tilde fence alone', () => {
      const input = '~~~\nnew\\.txt\n~~~\nafter new\\.txt'
      expect(stripNeedlessEscapes(input)).toBe('~~~\nnew\\.txt\n~~~\nafter new.txt')
    })

    it('does not let a tilde close a backtick fence', () => {
      const input = '```\nnew\\.txt\n~~~\nstill new\\.txt\n```\nout new\\.txt'
      expect(stripNeedlessEscapes(input)).toBe('```\nnew\\.txt\n~~~\nstill new\\.txt\n```\nout new.txt')
    })

    it('handles a double-backtick span holding a backtick', () => {
      expect(stripNeedlessEscapes('a new\\.txt ``x ` new\\.txt`` b new\\.txt'))
        .toBe('a new.txt ``x ` new\\.txt`` b new.txt')
    })

    it('treats an unclosed run as opening nothing that ends', () => {
      expect(stripNeedlessEscapes('a new\\.txt ` b new\\.txt')).toBe('a new.txt ` b new\\.txt')
    })

    it('leaves a fence that the document never closes', () => {
      expect(stripNeedlessEscapes('before new\\.txt\n```\nnew\\.txt')).toBe('before new.txt\n```\nnew\\.txt')
    })

    it('keeps an indented fence recognizable', () => {
      const input = '   ```\nnew\\.txt\n   ```\nafter new\\.txt'
      expect(stripNeedlessEscapes(input)).toBe('   ```\nnew\\.txt\n   ```\nafter new.txt')
    })

    // The serializer escapes a literal backtick of ordinary text with a backslash. That
    // backtick opens no code span. A scan that takes it for a delimiter pairs it with
    // the opening backtick of a real span and treats that span as ordinary text.
    describe('a literal backtick of ordinary text', () => {
      it('does not open a span that swallows the next real span', () => {
        expect(stripNeedlessEscapes('a lone \\` here, then `x\\_1` and `raw\\.json`'))
          .toBe('a lone \\` here, then `x\\_1` and `raw\\.json`')
      })

      it('still strips the needless escapes of the text around it', () => {
        expect(stripNeedlessEscapes('new\\.txt \\` snake\\_case `raw\\.json` and show\\.me'))
          .toBe('new.txt \\` snake_case `raw\\.json` and show.me')
      })

      it('keeps every real span after more than one literal backtick', () => {
        expect(stripNeedlessEscapes('\\` one \\` two `a\\_b` three `c\\.d`'))
          .toBe('\\` one \\` two `a\\_b` three `c\\.d`')
      })

      it('lets a literal backtick sit directly before a real span', () => {
        expect(stripNeedlessEscapes('\\``new\\.txt` and new\\.txt'))
          .toBe('\\``new\\.txt` and new.txt')
      })

      it('lets a literal backtick sit directly after a real span', () => {
        expect(stripNeedlessEscapes('`new\\.txt`\\` new\\.txt'))
          .toBe('`new\\.txt`\\` new.txt')
      })

      it('lets a literal backtick sit at the end of the line', () => {
        expect(stripNeedlessEscapes('`a\\_b` and new\\.txt \\`'))
          .toBe('`a\\_b` and new.txt \\`')
      })

      // Two backslashes are a backslash of the reader. The backtick behind them is a
      // delimiter, so the span that it opens stays verbatim.
      it('treats a backtick behind a backslash of the reader as a delimiter', () => {
        expect(stripNeedlessEscapes('a \\\\`x\\_1` and new\\.txt'))
          .toBe('a \\\\`x\\_1` and new.txt')
      })

      it('treats a backtick behind three backslashes as a literal backtick', () => {
        expect(stripNeedlessEscapes('a \\\\\\` b `x\\_1`'))
          .toBe('a \\\\\\` b `x\\_1`')
      })

      // A backslash inside a span is code. It does not escape the backtick that
      // closes the span.
      it('lets a backslash end a span without escaping its closing backtick', () => {
        expect(stripNeedlessEscapes('`a\\` new\\.txt and `b\\_c`'))
          .toBe('`a\\` new.txt and `b\\_c`')
      })

      it('keeps the lines of a fenced block that follows a line with a literal backtick', () => {
        expect(stripNeedlessEscapes('lone \\` and new\\.txt\n```\nnew\\.txt\n```\nlone \\` and new\\.txt'))
          .toBe('lone \\` and new.txt\n```\nnew\\.txt\n```\nlone \\` and new.txt')
      })
    })
  })

  it('leaves empty input alone', () => {
    expect(stripNeedlessEscapes('')).toBe('')
  })

  // The serializer escapes EVERY `_` of ordinary text. An underscore run between two
  // letters or digits can neither open nor close emphasis (CommonMark flanking rules),
  // so the escape protects nothing and only corrupts an identifier the agent receives.
  describe('underscores inside a word', () => {
    it('undoes the escape of an identifier that the reader typed', () => {
      expect(stripNeedlessEscapes('Keep COMMANDCODE\\_MODE\\_CONTEXT before the native process restarts.'))
        .toBe('Keep COMMANDCODE_MODE_CONTEXT before the native process restarts.')
    })

    it('undoes the escape between digits and between a digit and a letter', () => {
      expect(stripNeedlessEscapes('1\\_000\\_000 and x\\_1\\_y\\_2')).toBe('1_000_000 and x_1_y_2')
    })

    it('undoes every escape of a run', () => {
      expect(stripNeedlessEscapes('a\\_\\_b\\_\\_c')).toBe('a__b__c')
    })

    it('undoes the escape between letters that are not ASCII', () => {
      expect(stripNeedlessEscapes('café\\_naïve\\_日本')).toBe('café_naïve_日本')
    })

    it('undoes the escape of an underscore and of a dot in one line', () => {
      expect(stripNeedlessEscapes('Create new\\.txt for the snake\\_case name')).toBe('Create new.txt for the snake_case name')
    })

    // These underscores sit next to whitespace, punctuation, or a line edge, where
    // they can still delimit emphasis. Each escape stays.
    it('keeps an escape that has no letter or digit on both sides', () => {
      expect(stripNeedlessEscapes('\\_lead trail\\_ spaced \\_ gap a\\_(b) c\\_. d\\_\\_ e'))
        .toBe('\\_lead trail\\_ spaced \\_ gap a\\_(b) c\\_. d\\_\\_ e')
    })

    it('keeps an escape that follows an emphasis delimiter', () => {
      expect(stripNeedlessEscapes('_a_\\_b')).toBe('_a_\\_b')
    })

    it('keeps an escape at the start and at the end of a line', () => {
      expect(stripNeedlessEscapes('word\\_\n\\_word')).toBe('word\\_\n\\_word')
    })

    // A backslash of the reader serializes as two characters, so the escape of the
    // underscore that follows it has a backslash on its left, not a letter.
    it('keeps the escape that follows a backslash of the reader', () => {
      expect(stripNeedlessEscapes('a\\\\\\_b')).toBe('a\\\\\\_b')
    })

    it('leaves an inline code span alone', () => {
      expect(stripNeedlessEscapes('a\\_b `c\\_d` e\\_f')).toBe('a_b `c\\_d` e_f')
    })

    it('does not read across an inline code span', () => {
      expect(stripNeedlessEscapes('a\\_`b`\\_c')).toBe('a\\_`b`\\_c')
    })

    it('leaves a fenced block alone', () => {
      expect(stripNeedlessEscapes('a\\_b\n```\nc\\_d\n```\ne\\_f')).toBe('a_b\n```\nc\\_d\n```\ne_f')
    })
  })
})
