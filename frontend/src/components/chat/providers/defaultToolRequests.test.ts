import type { ToolRequestOverrides } from './defaultToolRequests'
import { describe, expect, it } from 'vitest'
import { DEFAULT_TOOL_REQUESTS, toolRequestFor } from './defaultToolRequests'

// The key VOCABULARY of the shared table, pinned for the seven kinds that carry the most
// argument spellings. The table is the one place each spelling lives, so a change that
// moves an entry into a provider's own table must carry every key with it. Nothing else
// can see that: the entries are total by type, and an entry that reads one key fewer
// still compiles, still draws, and simply states an empty field.

describe('DEFAULT_TOOL_REQUESTS', () => {
  it('reads a file read from the three path spellings, and its window', () => {
    expect(DEFAULT_TOOL_REQUESTS.read({ filePath: '/a.ts', path: '/b.ts', file_path: '/c.ts', offset: 10, limit: 5 }))
      .toEqual({ path: '/a.ts', offset: 10, limit: 5 })
    expect(DEFAULT_TOOL_REQUESTS.read({ path: '/b.ts' })).toEqual({ path: '/b.ts', offset: undefined, limit: undefined })
    expect(DEFAULT_TOOL_REQUESTS.read({ file_path: '/c.ts' })).toEqual({ path: '/c.ts', offset: undefined, limit: undefined })
    expect(DEFAULT_TOOL_REQUESTS.read({})).toEqual({ path: '', offset: undefined, limit: undefined })
  })

  it('reads a command from command and cmd, and keeps its description apart', () => {
    expect(DEFAULT_TOOL_REQUESTS.execute({ command: 'ls -l', cmd: 'pwd', description: 'List the directory' }))
      .toEqual({ command: 'ls -l', description: 'List the directory' })
    expect(DEFAULT_TOOL_REQUESTS.execute({ cmd: 'pwd' })).toEqual({ command: 'pwd', description: undefined })
    expect(DEFAULT_TOOL_REQUESTS.execute({})).toEqual({ command: '', description: undefined })
  })

  it('reads a glob from pattern and query, and its targets from every path spelling', () => {
    expect(DEFAULT_TOOL_REQUESTS.glob({ pattern: '*.ts', query: '*.md', paths: ['/src'] }))
      .toEqual({ pattern: '*.ts', paths: ['/src'] })
    expect(DEFAULT_TOOL_REQUESTS.glob({ query: '*.md', file_path: '/src/a.ts' }))
      .toEqual({ pattern: '*.md', paths: ['/src/a.ts'] })
    expect(DEFAULT_TOOL_REQUESTS.glob({})).toEqual({ pattern: '', paths: [] })
  })

  it('reads a grep from pattern and query, and its targets from every path spelling', () => {
    expect(DEFAULT_TOOL_REQUESTS.grep({ pattern: 'needle', query: 'haystack', paths: ['/src'] }))
      .toEqual({ pattern: 'needle', paths: ['/src'] })
    expect(DEFAULT_TOOL_REQUESTS.grep({ query: 'haystack', filePath: '/src/a.ts' }))
      .toEqual({ pattern: 'haystack', paths: ['/src/a.ts'] })
    expect(DEFAULT_TOOL_REQUESTS.grep({})).toEqual({ pattern: '', paths: [] })
  })

  it('reads a sent message from text and message, beside its recipient and summary', () => {
    expect(DEFAULT_TOOL_REQUESTS.message({ to: 'the lead', text: 'Ready', message: 'Stale', summary: 'Status' }))
      .toEqual({ to: 'the lead', text: 'Ready', summary: 'Status' })
    expect(DEFAULT_TOOL_REQUESTS.message({ message: 'Ready' })).toEqual({ to: undefined, text: 'Ready', summary: undefined })
    expect(DEFAULT_TOOL_REQUESTS.message({})).toEqual({ to: undefined, text: '', summary: undefined })
  })

  // Five spellings for three fields. They read the arguments alone and they read them
  // the way every provider spells them, so this entry is where they live -- ZCode's
  // override used to carry a second copy of all three beside its own `action`.
  it('reads a trigger id, a label and a schedule from every spelling, and states no action', () => {
    expect(DEFAULT_TOOL_REQUESTS.trigger({ trigger_id: 't-1', triggerId: 'never read', id: 'never read', name: 'Nightly', schedule: '0 0 * * *', cron: 'never read' }))
      .toEqual({ action: 'other', triggerId: 't-1', name: 'Nightly', schedule: '0 0 * * *' })
    expect(DEFAULT_TOOL_REQUESTS.trigger({ triggerId: 't-2', id: 'never read', cron: '@daily' }))
      .toEqual({ action: 'other', triggerId: 't-2', name: undefined, schedule: '@daily' })
    expect(DEFAULT_TOOL_REQUESTS.trigger({ id: 't-3' }))
      .toEqual({ action: 'other', triggerId: 't-3', name: undefined, schedule: undefined })
    expect(DEFAULT_TOOL_REQUESTS.trigger({}))
      .toEqual({ action: 'other', triggerId: undefined, name: undefined, schedule: undefined })
  })

  // The four file-change kinds read ONE key list for the file, and `edit` and `write`
  // read two more for the two sides. The row composes its header from the change list
  // at EVERY state of the call, so the entries that answered `{ changes: [] }` drew a
  // file change that could state no file -- not while it ran, not when it failed, and
  // not when it finished. `delete` and `move` sat beside them and already read theirs.
  it('reads an edit from every path spelling and every spelling of the two sides', () => {
    expect(DEFAULT_TOOL_REQUESTS.edit({ filePath: '/a.ts', path: '/b.ts', oldText: 'before', newText: 'after' }))
      .toStrictEqual({ changes: [{ filePath: '/a.ts', operation: 'edit', oldStr: 'before', newStr: 'after', structuredPatch: null }] })
    expect(DEFAULT_TOOL_REQUESTS.edit({ path: '/b.ts', oldString: 'before', newString: 'after' }))
      .toStrictEqual({ changes: [{ filePath: '/b.ts', operation: 'edit', oldStr: 'before', newStr: 'after', structuredPatch: null }] })
    expect(DEFAULT_TOOL_REQUESTS.edit({ file_path: '/c.ts', old_string: 'before', new_string: 'after' }))
      .toStrictEqual({ changes: [{ filePath: '/c.ts', operation: 'edit', oldStr: 'before', newStr: 'after', structuredPatch: null }] })
  })

  // A write ADDS the body it carries, so its one entry states that operation. The row
  // reads it: `RequestedFileChanges` heads such a change with the word "Create".
  it('reads a write as an addition, from the same keys', () => {
    expect(DEFAULT_TOOL_REQUESTS.write({ file_path: '/new.ts', new_string: 'export const a = 1\n' }))
      .toStrictEqual({ changes: [{ filePath: '/new.ts', operation: 'add', oldStr: '', newStr: 'export const a = 1\n', structuredPatch: null }] })
  })

  // The FILE is what the header needs, and a call that states one and no replacement
  // text still states it. The change draws no diff, and `RequestedFileChanges` gives
  // exactly that case a line of its own.
  it('states the file of an edit that carries only half of the change', () => {
    expect(DEFAULT_TOOL_REQUESTS.edit({ path: '/b.ts', newText: 'only the new side' }))
      .toStrictEqual({ changes: [{ filePath: '/b.ts', operation: 'edit', oldStr: '', newStr: 'only the new side', structuredPatch: null }] })
    expect(DEFAULT_TOOL_REQUESTS.edit({ path: '/b.ts' }))
      .toStrictEqual({ changes: [{ filePath: '/b.ts', operation: 'edit', oldStr: '', newStr: '', structuredPatch: null }] })
  })

  // No file, no change: the arguments state nothing the row could head itself with,
  // which is the one case an empty list describes truthfully.
  it('states no change for an edit or a write whose arguments state no file', () => {
    expect(DEFAULT_TOOL_REQUESTS.edit({ oldText: 'before', newText: 'after' })).toStrictEqual({ changes: [] })
    expect(DEFAULT_TOOL_REQUESTS.write({ new_string: 'a body with no file' })).toStrictEqual({ changes: [] })
    expect(DEFAULT_TOOL_REQUESTS.edit({})).toStrictEqual({ changes: [] })
    expect(DEFAULT_TOOL_REQUESTS.write({})).toStrictEqual({ changes: [] })
  })

  // A removal has nothing to diff, so the entry states the operation and the file
  // and no content. An agent answers a delete with a status and nothing else, so
  // without the entry the row drew the word "Delete" and stated no file at all.
  it('reads a delete from every path spelling, and states no change without a file', () => {
    expect(DEFAULT_TOOL_REQUESTS.delete({ filePath: '/a.ts' }))
      .toStrictEqual({ changes: [{ filePath: '/a.ts', operation: 'delete', oldStr: '', newStr: '', structuredPatch: null }] })
    expect(DEFAULT_TOOL_REQUESTS.delete({ path: '/b.ts' }))
      .toStrictEqual({ changes: [{ filePath: '/b.ts', operation: 'delete', oldStr: '', newStr: '', structuredPatch: null }] })
    expect(DEFAULT_TOOL_REQUESTS.delete({ file_path: '/c.ts' }))
      .toStrictEqual({ changes: [{ filePath: '/c.ts', operation: 'delete', oldStr: '', newStr: '', structuredPatch: null }] })
    expect(DEFAULT_TOOL_REQUESTS.delete({})).toStrictEqual({ changes: [] })
  })

  // A move states two paths and neither is a `filePath`, so it reads the source
  // and the destination from the two lists that carry those spellings. The row
  // files the change under the DESTINATION, which is where the file lands.
  it('reads a move from the source and destination spellings, and files it under the destination', () => {
    const moved = { filePath: '/new.ts', previousPath: '/old.ts', operation: 'move', oldStr: '', newStr: '', structuredPatch: null }
    expect(DEFAULT_TOOL_REQUESTS.move({ sourcePath: '/old.ts', destinationPath: '/new.ts' }))
      .toStrictEqual({ changes: [moved] })
    expect(DEFAULT_TOOL_REQUESTS.move({ source_path: '/old.ts', destination_path: '/new.ts' }))
      .toStrictEqual({ changes: [moved] })
    expect(DEFAULT_TOOL_REQUESTS.move({ oldPath: '/old.ts', newPath: '/new.ts' }))
      .toStrictEqual({ changes: [moved] })
    // The file-path list is the destination's last resort, for a provider that
    // spells the destination the way a read spells its file.
    expect(DEFAULT_TOOL_REQUESTS.move({ source_path: '/old.ts', path: '/new.ts' }))
      .toStrictEqual({ changes: [moved] })
  })

  // One path alone still identifies the file the row is about, so the entry
  // stands with the other side absent. A source the destination repeats names
  // one file twice, and a `previousPath` equal to `filePath` would draw a move
  // arrow that starts where it ends.
  it('states the source alone for a move that names no destination, and drops a previousPath the destination repeats', () => {
    // An absent previousPath is an omitted key, never an explicit undefined.
    expect(DEFAULT_TOOL_REQUESTS.move({ sourcePath: '/old.ts' }))
      .toStrictEqual({ changes: [{ filePath: '/old.ts', operation: 'move', oldStr: '', newStr: '', structuredPatch: null }] })
    expect(DEFAULT_TOOL_REQUESTS.move({ sourcePath: '/same.ts', destinationPath: '/same.ts' }))
      .toStrictEqual({ changes: [{ filePath: '/same.ts', operation: 'move', oldStr: '', newStr: '', structuredPatch: null }] })
    expect(DEFAULT_TOOL_REQUESTS.move({})).toStrictEqual({ changes: [] })
  })

  // `mode` and `target` are two separate facts, not two spellings of one: the mode is
  // where the session lands, and the target is what the switch acts on.
  it('reads a mode switch from mode and targetModeId, and keeps its target apart', () => {
    expect(DEFAULT_TOOL_REQUESTS.switch_mode({ mode: 'plan', targetModeId: 'agent', target: 'feature-branch' }))
      .toEqual({ mode: 'plan', target: 'feature-branch' })
    expect(DEFAULT_TOOL_REQUESTS.switch_mode({ targetModeId: 'agent' })).toEqual({ mode: 'agent', target: undefined })
    expect(DEFAULT_TOOL_REQUESTS.switch_mode({})).toEqual({ mode: undefined, target: undefined })
  })
})

describe('toolRequestFor', () => {
  interface Facts { text: string }
  const facts: Facts = { text: 'the collected text' }

  it('reads the shared entry for a kind the overrides list nowhere', () => {
    expect(toolRequestFor('read', { path: '/a.ts' }, facts, {}))
      .toEqual({ path: '/a.ts', offset: undefined, limit: undefined })
  })

  it('reads the provider entry, from the facts the shared entry cannot see', () => {
    const overrides: ToolRequestOverrides<Facts> = {
      think: (args, own) => ({ text: typeof args.thought === 'string' ? args.thought : own.text }),
    }
    expect(toolRequestFor('think', {}, facts, overrides)).toEqual({ text: 'the collected text' })
    expect(toolRequestFor('think', { thought: 'stated in the arguments' }, facts, overrides))
      .toEqual({ text: 'stated in the arguments' })
    // The shared entry states the empty text, so the two answers genuinely differ.
    expect(DEFAULT_TOOL_REQUESTS.think({})).toEqual({ text: '' })
  })

  it('shadows no kind beside the ones the overrides list', () => {
    const overrides: ToolRequestOverrides<Facts> = { think: () => ({ text: 'the provider answer' }) }
    expect(toolRequestFor('message', { text: 'Ready' }, facts, overrides))
      .toEqual({ to: undefined, text: 'Ready', summary: undefined })
    expect(toolRequestFor('execute', { command: 'ls' }, facts, overrides))
      .toEqual({ command: 'ls', description: undefined })
  })
})
