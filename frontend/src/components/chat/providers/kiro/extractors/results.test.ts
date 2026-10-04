import { describe, expect, it } from 'vitest'
import { kiroCommandExit, kiroCommandOutput, kiroFileSearchResult, kiroGrepResult, kiroListResult, kiroRequestedTodoItems, kiroTodoItems } from './results'

describe('kiroCommandExit', () => {
  it('reads the exit code that Kiro states beside the output', () => {
    expect(kiroCommandExit({ rawOutput: { output: 'x', exitCode: 2 } })).toEqual({ exitCode: 2 })
    expect(kiroCommandExit({ rawOutput: { output: 'x' } })).toBeUndefined()
    expect(kiroCommandExit({ rawOutput: 'text' })).toBeUndefined()
    expect(kiroCommandExit({})).toBeUndefined()
  })
})

describe('kiroCommandOutput', () => {
  it('reads the output alone, and keeps an empty one', () => {
    expect(kiroCommandOutput({ rawOutput: { output: 'v3-shell\n', exitCode: 0, message: 'Output:\nv3-shell\n' } })).toBe('v3-shell\n')
    expect(kiroCommandOutput({ rawOutput: { output: '' } })).toBe('')
    expect(kiroCommandOutput({ rawOutput: { message: 'x' } })).toBeUndefined()
  })
})

describe('kiroFileSearchResult', () => {
  it('reads the files between the two rules', () => {
    const result = kiroFileSearchResult('You searched for hello and received the following complete results:\n---\nhello.txt\nsrc/hello.ts\n---')
    expect(result).toMatchObject({ filenames: ['hello.txt', 'src/hello.ts'], numFiles: 2, truncated: false, empty: false })
  })

  it('states a list that Kiro cut', () => {
    const result = kiroFileSearchResult('You searched for * and received the following incomplete results:\n---\na\n---\nRefine your search, or use the excludePattern to retrieve all results.')
    expect(result).toMatchObject({ filenames: ['a'], truncated: true })
  })

  it('reads the sentence of an empty search as no file', () => {
    expect(kiroFileSearchResult('You searched for x and received the following complete results:\n---\nNo files found matching your search.\n---')).toMatchObject({ filenames: [], empty: true })
  })

  it('skips an empty line inside the list, and reads a header that states no completeness', () => {
    const result = kiroFileSearchResult('You searched for a and received the following results:\n---\na.ts\n\nb.ts\n---')
    expect(result).toMatchObject({ filenames: ['a.ts', 'b.ts'], numFiles: 2, truncated: false })
  })

  it('answers null for another text', () => {
    expect(kiroFileSearchResult('You searched for x and received the error: boom')).toBeNull()
    expect(kiroFileSearchResult('You searched for x and received the following complete results:\nno rule')).toBeNull()
    expect(kiroFileSearchResult('You searched for x and received the following complete results:\n---\nno end')).toBeNull()
  })
})

describe('kiroGrepResult', () => {
  it('reads the matches of each file, and skips a context line', () => {
    const result = kiroGrepResult('You searched for hello and received the following results:\na.txt\n1:hello world\n2-context\n\nsrc/b.ts\n10:say hello')
    expect(result).toMatchObject({
      filenames: ['a.txt', 'src/b.ts'],
      lines: [{ filePath: 'a.txt', lineNumber: 1, text: 'hello world' }, { filePath: 'src/b.ts', lineNumber: 10, text: 'say hello' }],
      matchCount: 2,
      truncated: false,
      empty: false,
    })
  })

  it('reads a text that states no match', () => {
    expect(kiroGrepResult('You searched for x and received the following results:\nNo matches found.')).toMatchObject({ filenames: [], matchCount: 0, empty: true })
  })

  it('states a result that Kiro cut', () => {
    expect(kiroGrepResult('You searched for x and received the following results:\na\n1:x\n\n... [truncated: too many matches] ...\n')).toMatchObject({ truncated: true, matchCount: 1 })
  })

  it('keeps a colon inside the text of a match', () => {
    expect(kiroGrepResult('You searched for a and received the following results:\nf.ts\n3:a: b\n')?.lines).toEqual([{ filePath: 'f.ts', lineNumber: 3, text: 'a: b' }])
  })

  // A file whose lines are all context holds no match of its own.
  it('states a file of context lines alone as a search with no match', () => {
    expect(kiroGrepResult('You searched for a and received the following results:\nf.ts\n2-around')).toMatchObject({ filenames: ['f.ts'], lines: [], matchCount: 0, empty: true })
  })

  it('answers null for another text', () => {
    expect(kiroGrepResult('You searched for x and received the error boom')).toBeNull()
    expect(kiroGrepResult('plain words')).toBeNull()
  })
})

describe('kiroListResult', () => {
  it('reads each entry, and marks a directory', () => {
    expect(kiroListResult('Contents of /w:\n  [FILE] hello.txt\n  [DIR] src')).toEqual({ entries: [{ path: 'hello.txt' }, { path: 'src/' }] })
  })

  it('reads a listing that ends with a line break', () => {
    expect(kiroListResult('Contents of /w:\n  [FILE] a\n')).toEqual({ entries: [{ path: 'a' }] })
    expect(kiroListResult('Contents of /w:\n  [FILE] a\n\n')).toEqual({ entries: [{ path: 'a' }] })
  })

  it('reads an empty listing', () => {
    expect(kiroListResult('Contents of /w:')).toEqual({ entries: [] })
    expect(kiroListResult('Contents of /w:\n')).toEqual({ entries: [] })
  })

  it('states an empty or absent directory', () => {
    expect(kiroListResult('Directory /w/none is empty or does not exist.')).toEqual({ entries: [], notice: 'Directory /w/none is empty or does not exist.' })
    expect(kiroListResult('Directory /w/none is empty or does not exist.\n')).toEqual({ entries: [], notice: 'Directory /w/none is empty or does not exist.' })
  })

  it('answers null for an empty line inside the listing', () => {
    expect(kiroListResult('Contents of /w:\n  [FILE] a\n\n  [FILE] b')).toBeNull()
  })

  it('answers null for another format', () => {
    expect(kiroListResult('src/\n  a.ts')).toBeNull()
    expect(kiroListResult('Contents of /w:\n- a.ts')).toBeNull()
  })
})

describe('kiroTodoItems', () => {
  it('reads each task as done or pending, with its details', () => {
    expect(kiroTodoItems([
      { id: '1', task_description: 'one', details: ' first ', completed: true },
      { id: '2', task_description: 'two', completed: false },
      { id: '3', task_description: '  ' },
      'x',
    ])).toEqual([
      { id: '1', rowKey: '1', content: 'one', activeForm: '', description: 'first', status: 'completed' },
      { id: '2', rowKey: '2', content: 'two', activeForm: '', status: 'pending' },
    ])
  })

  it('answers no item for a value that is no list', () => {
    expect(kiroTodoItems(undefined)).toEqual([])
    expect(kiroTodoItems({ 0: {} })).toEqual([])
  })

  // Only `true` completes a task: Kiro states a boolean, and a word is no answer.
  it('reads a task as pending unless Kiro states it completed, and omits an absent id and blank details', () => {
    const [item] = kiroTodoItems([{ task_description: 'one', completed: 'true', details: '  ' }])
    expect(item).toMatchObject({ content: 'one', status: 'pending' })
    expect(item && Object.hasOwn(item, 'id')).toBe(false)
    expect(item && Object.hasOwn(item, 'description')).toBe(false)
    expect(item?.rowKey).not.toBe('')
  })
})

describe('kiroRequestedTodoItems', () => {
  it('reads the tasks a create asks for, in their order', () => {
    expect(kiroRequestedTodoItems({ command: 'create', tasks: { 1: { task_description: 'two' }, 0: { task_description: 'one' }, x: { task_description: 'skip' } } }).map(item => item.content))
      .toEqual(['one', 'two'])
  })

  it('reads the tasks of the list that the model sends', () => {
    expect(kiroRequestedTodoItems({ command: 'add', tasks: [{ task_description: 'one' }, { task_description: 'two', details: 'd' }] }).map(item => item.content))
      .toEqual(['one', 'two'])
  })

  // The keys are positions, so `10` comes after `2`, which a text sort would reverse.
  it('orders the keyed tasks by number, and skips an entry that is no task', () => {
    expect(kiroRequestedTodoItems({ tasks: { 10: { task_description: 'eleventh' }, 2: { task_description: 'third' }, 3: 'noise' } }).map(item => item.content))
      .toEqual(['third', 'eleventh'])
  })

  it('reads no task for a command that asks about tasks by id', () => {
    expect(kiroRequestedTodoItems({ command: 'complete', completed_task_ids: { 0: '1' } })).toEqual([])
  })
})
