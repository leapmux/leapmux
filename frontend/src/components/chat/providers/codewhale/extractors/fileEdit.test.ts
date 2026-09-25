import { describe, expect, it } from 'vitest'
import { CODEWHALE_TOOL } from '~/generated/contracts/codewhale-protocol'
import { codewhaleDiffSections, codewhaleMutationChanges, codewhaleRequestedChanges } from './fileEdit'

const A_DIFF = '--- a/a.ts\n+++ b/a.ts\n@@ -1 +1 @@\n-before\n+after\n'
const B_DIFF = '--- a/b.ts\n+++ b/b.ts\n@@ -0,0 +1 @@\n+new\n'
const GONE_DIFF = '--- a/gone.ts\n+++ /dev/null\n@@ -1 +0,0 @@\n-old\n'

describe('codewhaleDiffSections', () => {
  it('splits a combined diff at each header pair', () => {
    const sections = codewhaleDiffSections(`${A_DIFF}${B_DIFF}`)
    expect([...sections.keys()]).toStrictEqual(['a.ts', 'b.ts'])
    // The next file's `--- a/` header never lands in the previous file's hunks.
    expect(sections.get('a.ts')).toBe('@@ -1 +1 @@\n-before\n+after')
    expect(sections.get('b.ts')).toBe('@@ -0,0 +1 @@\n+new\n')
  })

  it('keys a deletion by its old side', () => {
    expect([...codewhaleDiffSections(GONE_DIFF).keys()]).toStrictEqual(['gone.ts'])
  })

  it('ends a section at a git header and reads no section from text with no header', () => {
    const sections = codewhaleDiffSections(`diff --git a/x b/y\nsimilarity index 100%\n${A_DIFF}`)
    expect([...sections.keys()]).toStrictEqual(['a.ts'])
    expect(codewhaleDiffSections('@@ -1 +1 @@\n-a\n+b').size).toBe(0)
    expect(codewhaleDiffSections('').size).toBe(0)
  })

  it('keys a creation by its new side', () => {
    expect([...codewhaleDiffSections('--- /dev/null\n+++ b/new.ts\n@@ -0,0 +1 @@\n+x\n').keys()]).toStrictEqual(['new.ts'])
  })

  // `diff -u` puts a tab and a timestamp after each header path.
  it('keys a section by the path alone when the header carries a timestamp', () => {
    const sections = codewhaleDiffSections('--- a/a.ts\t2026-01-01 00:00:00\n+++ b/a.ts\t2026-01-02 00:00:00\n@@ -1 +1 @@\n-x\n+y')
    expect([...sections.entries()]).toStrictEqual([['a.ts', '@@ -1 +1 @@\n-x\n+y']])
  })

  it('reads no section from a header pair that ends the text, and from a header that names no file', () => {
    expect(codewhaleDiffSections('--- a/a.ts\n+++ b/a.ts').size).toBe(0)
    expect(codewhaleDiffSections('--- /dev/null\n+++ /dev/null\n@@ -0,0 +0,0 @@').size).toBe(0)
  })

  // A header pair and its final newline split into an EMPTY section, and each reader
  // of a section treats an empty one as absent. Both spellings state no change.
  it('states no change for a header pair that states no hunks, with or without its final newline', () => {
    for (const diff of ['--- a/a.ts\n+++ b/a.ts\n', '--- a/a.ts\n+++ b/a.ts']) {
      expect(codewhaleRequestedChanges(CODEWHALE_TOOL.ApplyPatch, { patch: diff }), JSON.stringify(diff)).toStrictEqual([])
      expect(codewhaleRequestedChanges(CODEWHALE_TOOL.ApplyPatch, { path: 'a.ts', patch: diff }), JSON.stringify(diff)).toStrictEqual([])
      expect(codewhaleMutationChanges({ mutation: { diff, files: [{ path: 'a.ts', outcome: 'updated' }] } }), JSON.stringify(diff))
        .toStrictEqual([{ filePath: 'a.ts', structuredPatch: null, oldStr: '', newStr: '', operation: 'edit' }])
    }
  })

  // A hunk line whose text opens with `--` is a removed line only when the next line
  // is not a `+++` header, so a lone one stays in its section.
  it('keeps a removed line that reads like a header inside its section', () => {
    const sections = codewhaleDiffSections('--- a/q.sql\n+++ b/q.sql\n@@ -1,2 +1 @@\n--- a comment\n-select 1\n+select 2')
    expect(sections.get('q.sql')).toBe('@@ -1,2 +1 @@\n--- a comment\n-select 1\n+select 2')
  })
})

describe('codewhaleMutationChanges', () => {
  it('reads each file with its operation and its own hunks', () => {
    const changes = codewhaleMutationChanges({
      mutation: {
        diff: `${A_DIFF}${B_DIFF}${GONE_DIFF}`,
        files: [{ path: 'a.ts', outcome: 'updated' }, { path: 'b.ts', outcome: 'created' }, { path: 'gone.ts', outcome: 'deleted' }],
        renames: [],
      },
    })
    expect(changes?.map(change => [change.filePath, change.operation])).toStrictEqual([['a.ts', 'edit'], ['b.ts', 'add'], ['gone.ts', 'delete']])
    expect(changes?.[0]?.structuredPatch).toStrictEqual([{ oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, lines: ['-before', '+after'] }])
  })

  it('states a rename as a move from its old path', () => {
    const changes = codewhaleMutationChanges({ mutation: { diff: '', files: [], renames: [{ from: 'old.ts', to: 'new.ts' }] } })
    expect(changes).toStrictEqual([{ filePath: 'new.ts', previousPath: 'old.ts', operation: 'move', structuredPatch: null, oldStr: '', newStr: '' }])
  })

  it('drops a rename that states no new path, and keeps one that states no old path', () => {
    expect(codewhaleMutationChanges({ mutation: { renames: [{ from: 'old.ts' }, 'x'] } })).toBeNull()
    expect(codewhaleMutationChanges({ mutation: { renames: [{ to: 'new.ts' }] } }))
      .toStrictEqual([{ filePath: 'new.ts', operation: 'move', structuredPatch: null, oldStr: '', newStr: '' }])
  })

  // The walk keeps a file whose record states an operation, and drops only an
  // `edit` with no change beside a file that states one.
  it('drops an edit with no hunks when another file in the record states a change', () => {
    const changes = codewhaleMutationChanges({ mutation: { diff: B_DIFF, files: [{ path: 'a.ts', outcome: 'updated' }, { path: 'b.ts', outcome: 'created' }] } })
    expect(changes?.map(change => [change.filePath, change.operation])).toStrictEqual([['b.ts', 'add']])
  })

  it('reads a record in another shape as no record', () => {
    expect(codewhaleMutationChanges({ mutation: 'a.ts' })).toBeNull()
    expect(codewhaleMutationChanges({ mutation: { files: 'a.ts', renames: {} } })).toBeNull()
  })

  it('keeps a file whose change states no hunks', () => {
    expect(codewhaleMutationChanges({ mutation: { diff: '', files: [{ path: 'a.ts', outcome: 'updated' }] } }))
      .toStrictEqual([{ filePath: 'a.ts', structuredPatch: null, oldStr: '', newStr: '', operation: 'edit' }])
  })

  it('finds the hunks of an absolute path, and of a path the header spells another way', () => {
    // The runtime prefixes the REQUESTED path, so an absolute one becomes `a//abs`.
    const absolute = codewhaleMutationChanges({ mutation: { diff: '--- a//w/a.ts\n+++ b//w/a.ts\n@@ -1 +1 @@\n-x\n+y\n', files: [{ path: '/w/a.ts', outcome: 'updated' }] } })
    expect(absolute?.[0]?.structuredPatch).toStrictEqual([{ oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, lines: ['-x', '+y'] }])
    const relative = codewhaleMutationChanges({ mutation: { diff: A_DIFF, files: [{ path: './a.ts', outcome: 'updated' }] } })
    expect(relative?.[0]?.structuredPatch).toStrictEqual([{ oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, lines: ['-before', '+after'] }])
  })

  it('reads an unknown outcome word as an edit', () => {
    expect(codewhaleMutationChanges({ mutation: { diff: A_DIFF, files: [{ path: 'a.ts', outcome: 'a_later_word' }] } })?.[0]?.operation).toBe('edit')
  })

  it('answers null for a result that states no file', () => {
    expect(codewhaleMutationChanges({})).toBeNull()
    expect(codewhaleMutationChanges({ mutation: { diff: A_DIFF, files: [], renames: [] } })).toBeNull()
    expect(codewhaleMutationChanges({ mutation: { files: [{ outcome: 'updated' }, 'x'] } })).toBeNull()
  })
})

describe('codewhaleRequestedChanges', () => {
  it('states a write as the whole new file', () => {
    expect(codewhaleRequestedChanges(CODEWHALE_TOOL.Write, { path: 'a.ts', content: 'x' }))
      .toStrictEqual([{ filePath: 'a.ts', structuredPatch: null, oldStr: '', newStr: 'x', operation: 'add' }])
  })

  it('states one change for each substitution, and the legacy single one', () => {
    expect(codewhaleRequestedChanges(CODEWHALE_TOOL.Edit, { path: 'a.ts', edits: [{ oldText: 'a', newText: 'b' }, 'x'] }))
      .toStrictEqual([{ filePath: 'a.ts', structuredPatch: null, oldStr: 'a', newStr: 'b' }])
    expect(codewhaleRequestedChanges(CODEWHALE_TOOL.EditFile, { path: 'a.ts', old_string: 'a', new_string: 'b' }))
      .toStrictEqual([{ filePath: 'a.ts', structuredPatch: null, oldStr: 'a', newStr: 'b' }])
  })

  it('reads every form a patch arrives in', () => {
    // A unified diff with a header pair for each file.
    expect(codewhaleRequestedChanges(CODEWHALE_TOOL.ApplyPatch, { patch: `${A_DIFF}${B_DIFF}` }).map(change => change.filePath)).toStrictEqual(['a.ts', 'b.ts'])
    // Hunks alone, for the file `path` states.
    expect(codewhaleRequestedChanges(CODEWHALE_TOOL.ApplyPatch, { path: 'c.ts', patch: '@@ -1 +1 @@\n-a\n+b' }).map(change => change.filePath)).toStrictEqual(['c.ts'])
    // Whole-file replacements, under both spellings.
    expect(codewhaleRequestedChanges(CODEWHALE_TOOL.ApplyPatch, { replace: [{ path: 'd.ts', content: 'x' }], changes: [{ path: 'e.ts', content: 'y' }] }).map(change => change.filePath)).toStrictEqual(['d.ts', 'e.ts'])
    // The envelope a model sends although the tool refuses it.
    expect(codewhaleRequestedChanges(CODEWHALE_TOOL.ApplyPatch, { patch: '*** Begin Patch\n*** Add File: f.txt\n+hi\n*** End Patch\n' }).map(change => change.filePath)).toStrictEqual(['f.txt'])
    // The facade's patch action.
    expect(codewhaleRequestedChanges(CODEWHALE_TOOL.File, { action: 'patch', patch: A_DIFF }).map(change => change.filePath)).toStrictEqual(['a.ts'])
  })

  it('answers no change for arguments that name no file', () => {
    expect(codewhaleRequestedChanges(CODEWHALE_TOOL.Edit, {})).toStrictEqual([])
    expect(codewhaleRequestedChanges(CODEWHALE_TOOL.ApplyPatch, { patch: '@@ -1 +1 @@\n-a\n+b' })).toStrictEqual([])
    expect(codewhaleRequestedChanges(CODEWHALE_TOOL.ApplyPatch, {})).toStrictEqual([])
  })

  it('reads the File facade\'s write and edit actions, and a write with no content as an empty file', () => {
    expect(codewhaleRequestedChanges(CODEWHALE_TOOL.File, { action: 'write', path: 'a.ts', content: 'x' }))
      .toStrictEqual([{ filePath: 'a.ts', structuredPatch: null, oldStr: '', newStr: 'x', operation: 'add' }])
    expect(codewhaleRequestedChanges(CODEWHALE_TOOL.File, { action: 'edit', path: 'a.ts', edits: [{ oldText: 'a', newText: 'b' }] }))
      .toStrictEqual([{ filePath: 'a.ts', structuredPatch: null, oldStr: 'a', newStr: 'b' }])
    expect(codewhaleRequestedChanges(CODEWHALE_TOOL.WriteFile, { path: 'empty.txt' }))
      .toStrictEqual([{ filePath: 'empty.txt', structuredPatch: null, oldStr: '', newStr: '', operation: 'add' }])
  })

  // Only the File facade states its operation in `action`. Any other tool that
  // carries the word keeps the operation its own name states.
  it('reads an action word as the operation only for the File facade', () => {
    expect(codewhaleRequestedChanges(CODEWHALE_TOOL.Edit, { action: 'write', path: 'a.ts', edits: [{ oldText: 'a', newText: 'b' }] }))
      .toStrictEqual([{ filePath: 'a.ts', structuredPatch: null, oldStr: 'a', newStr: 'b' }])
    expect(codewhaleRequestedChanges(CODEWHALE_TOOL.Edit, { action: 'patch', patch: A_DIFF })).toStrictEqual([])
  })

  it('keeps the whole-file replacements beside a path-scoped patch that does not parse', () => {
    expect(codewhaleRequestedChanges(CODEWHALE_TOOL.ApplyPatch, { path: 'c.ts', patch: 'not a patch', replace: [{ path: 'd.ts', content: 'x' }, { content: 'no path' }] }))
      .toStrictEqual([{ filePath: 'd.ts', structuredPatch: null, oldStr: '', newStr: 'x', operation: 'add' }])
    expect(codewhaleRequestedChanges(CODEWHALE_TOOL.ApplyPatch, { path: 'c.ts', patch: 'not a patch' })).toStrictEqual([])
  })

  it('states a multi-file patch and its replacements together, the patch first', () => {
    expect(codewhaleRequestedChanges(CODEWHALE_TOOL.ApplyPatch, { patch: A_DIFF, replace: [{ path: 'd.ts', content: 'x' }] }).map(change => change.filePath))
      .toStrictEqual(['a.ts', 'd.ts'])
  })
})
