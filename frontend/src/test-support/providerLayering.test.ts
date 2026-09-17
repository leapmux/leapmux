import { readFileSync, rmSync, writeFileSync } from 'node:fs'
import { basename, join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { lineNumberAt, stripCommentLines } from '~/test-support/sourceScan'
import { collectFiles, frontendRoot, posixRelative } from '~/test-support/sourceTree'
import { importEdgesIntoLayer, moduleImportEdges } from '~/test-support/typescriptImports'

// The layering guard for `src/components/chat/providers/` -- layer 1 of the chat
// render pipeline. A plugin reads its provider's own wire format and returns the
// shared row IR (layer 2); the shared renderers (layer 3) draw it. A plugin that
// draws a row of its own re-opens the split this pipeline exists to close: the
// same tool then looked one way on one provider and another way on the next, and
// a fix to the shared body reached only the providers that used it.
//
// The rule is about JSX, not about every component import. Three providers still
// ANSWER a request of their own -- Codex's decision words, Pi's dialog envelopes
// and Cursor's create-plan verdict -- so those action rows draw and stay listed
// below. Every other provider's control surface is gone: `extractControl` fills
// the shared control IR and the banner draws and answers it.

const PROVIDERS_DIR = join(frontendRoot, 'src/components/chat/providers')
const RESULTS_DIR = join(frontendRoot, 'src/components/chat/results')

/**
 * The provider modules that MAY draw.
 *
 * Each is a control surface: a permission prompt, a question form, a plan
 * approval. Those read a provider's own request payload and answer it, which is
 * not a transcript row -- the row IR does not describe them, and unifying them is
 * its own piece of work.
 */
const DRAWING_ALLOWED = new Set([
  'codex/CodexControlActions.tsx',
  'cursor/CursorControlActions.tsx',
  'pi/PiControlActions.tsx',
  'pi/PiPlanApprovalActions.tsx',
])

/** The plugin modules a guard reads: the implementation, never its own tests or fixtures. */
function providerModules(): string[] {
  return collectFiles(PROVIDERS_DIR, {
    matches: name => (name.endsWith('.ts') || name.endsWith('.tsx'))
      && !name.endsWith('.test.ts')
      && !name.endsWith('.test.tsx')
      // A `.fixtures.ts` module is test DATA co-located with the provider it
      // describes: the corpus each `toolResults.test.ts` replays. Nothing that ships
      // imports one, so a guard over the implementation must not read it.
      && !name.endsWith('.fixtures.ts')
      && name !== 'testUtils.tsx'
      && name !== 'testUtils.ts'
      && name !== 'testMocks.ts',
  })
}

/** The drawing modules a guard reads: the implementation, never its own tests or fixtures. */
function resultModules(): string[] {
  return collectFiles(RESULTS_DIR, {
    matches: name => (name.endsWith('.ts') || name.endsWith('.tsx'))
      && !name.endsWith('.test.ts')
      && !name.endsWith('.test.tsx')
      && !name.endsWith('.css.ts')
      && !name.endsWith('.fixtures.ts'),
  })
}

/**
 * Every import in one file whose path crosses into `<layer>/`.
 *
 * Read from the compiler's syntax tree (`typescriptImports.ts`), so every form the
 * grammar holds is an edge the rule sees -- static, re-export, side-effect,
 * `import()` and `require()`, in either quote style -- and an explanation that
 * quotes an import in a comment is never an offence. The two layering rules below
 * read opposite directions of the same edge, so they share the matcher.
 */
function importsFromLayer(file: string, layer: string): Array<{ path: string, line: number }> {
  const fileName = basename(file)
  return importEdgesIntoLayer(moduleImportEdges(readFileSync(file, 'utf8'), fileName), layer)
    .map(edge => ({ path: edge.specifier, line: edge.line }))
}

/** A line that opens or continues a JSX element, ignoring a generic type argument. */
const JSX_OPEN_RE = /(?:^|[\s(={[,?:])<[A-Z][\w.]*(?:\s|\/?>)/i

/**
 * Whether one module's source draws.
 *
 * A source scan rather than a parse, for the reason every rule beside it gives:
 * the guard must run in milliseconds over the whole tree. It reads an OPENING
 * element, which no type annotation can produce -- `Set<string>` has no space or
 * slash after the name, and `a < b` has no identifier that starts an element.
 *
 * ONE form does produce it: the type parameter list of a generic ARROW function.
 * ` <K extends ToolKind>` opens with a space, a capital and a space, so this rule
 * reads it as an element and reports a `.ts` file that holds no markup at all. Write
 * a function DECLARATION instead -- `function requestFor<K extends ToolKind>(...)`
 * puts the `<` against the name, where the rule does not look. Claude's `requestFor`
 * is that declaration, and records the reason at the site.
 */
function drawsJsx(source: string): number | null {
  const lines = source.split('\n')
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]
    if (line === undefined)
      continue
    // A comment can hold example markup, and a doc comment often does.
    if (/^\s*(?:\/\/|\*|\/\*)/.test(line))
      continue
    if (JSX_OPEN_RE.test(line))
      return i + 1
  }
  return null
}

describe('the chat provider layer', () => {
  it('finds the modules it guards', () => {
    expect(providerModules().length).toBeGreaterThan(30)
  })

  it('draws no transcript row of its own', () => {
    const offences: string[] = []
    for (const file of providerModules()) {
      const relative = posixRelative(PROVIDERS_DIR, file)
      if (DRAWING_ALLOWED.has(relative))
        continue
      const line = drawsJsx(readFileSync(file, 'utf8'))
      if (line !== null)
        offences.push(`${posixRelative(frontendRoot, file)}:${line}`)
    }
    expect(
      offences,
      'A plugin reads its provider\'s bytes into the shared row IR; the shared renderers draw it. '
      + 'Move the markup into `components/chat/results/` and state the call through `ToolCallIR`.',
    ).toEqual([])
  })

  // Drawing is one direction of the same rule. Reading the DRAWING layer is the
  // other: a plugin that imports from `results/` puts a parser the whole pipeline
  // depends on behind a module that exists to draw. Seven providers reached into
  // `results/readFileResult.tsx` for its content parser before this rule existed,
  // so the IR a provider produced was defined one layer above the IR.
  it('reads nothing from the drawing layer', () => {
    const offences: string[] = []
    for (const file of providerModules()) {
      for (const found of importsFromLayer(file, 'results'))
        offences.push(`${posixRelative(frontendRoot, file)}:${found.line} imports ${found.path}`)
    }
    expect(
      offences,
      'A plugin reads its provider\'s bytes into the IR, and the IR is layer 2. '
      + 'Move the pure helper into `components/chat/ir/` beside the type it builds.',
    ).toEqual([])
  })

  // The allowlist must stay pinned to files that exist. Left behind after a module
  // moves, it would silently forgive whatever new file takes that path.
  it('keeps every allowed drawing module pinned to a file that exists', () => {
    const present = new Set(providerModules().map(file => posixRelative(PROVIDERS_DIR, file)))
    const stale = [...DRAWING_ALLOWED].filter(entry => !present.has(entry))
    expect(stale, 'Delete the entry, or repoint it at the module that replaced it.').toEqual([])
  })
})

// Layer 3 of the same pipeline, and the other end of the import rule above. The
// README beside it states what it may know: the row IR and the design tokens. A
// renderer that imports a plugin reaches for one provider's parser inside the module
// that exists to draw the SAME row for every provider -- which re-opens the split the
// pipeline closed, from the end nothing watched. The `results/` -> `providers/`
// direction had no guard at all, although the README said this file carried one.

describe('the chat drawing layer', () => {
  it('finds the modules it guards', () => {
    expect(resultModules().length).toBeGreaterThan(20)
  })

  it('imports nothing from the plugin layer', () => {
    const offences: string[] = []
    for (const file of resultModules()) {
      for (const found of importsFromLayer(file, 'providers'))
        offences.push(`${posixRelative(frontendRoot, file)}:${found.line} imports ${found.path}`)
    }
    expect(
      offences,
      'A renderer draws the row IR, and the IR is layer 2. It never reads a plugin: the '
      + 'shape it needs belongs in `components/chat/ir/`, where both layers read it from.',
    ).toEqual([])
  })

  // The matcher IS both layering rules, and a guard that matches nothing reads exactly
  // like a tree that holds nothing: a green suite either way. This pins it to the text
  // it must find, and to the comment it must skip.
  it('finds an import of a layer, and reads none out of a comment', () => {
    const file = join(frontendRoot, 'src/components/chat/results/selfCheck.tsx')
    writeFileSync(file, [
      '// A renderer must not write `from \'~/components/chat/providers/registry\'`.',
      'import { providerFor } from \'~/components/chat/providers/registry\'',
      'import { toolCallRow } from \'~/components/chat/ir/row\'',
    ].join('\n'))
    try {
      expect(importsFromLayer(file, 'providers')).toEqual([{ path: '~/components/chat/providers/registry', line: 2 }])
      expect(importsFromLayer(file, 'results')).toEqual([])
    }
    finally {
      rmSync(file)
    }
  })
})

// The JSX rule above says a plugin must not DRAW. The two rules below say the
// opposite direction: shared code must not know which provider it serves, and must
// not spell a provider's own wire words. Both failures read the same way -- one
// provider works, and the next one silently takes the first one's behavior.

const SRC_DIR = join(frontendRoot, 'src')
const PROVIDERS_RELATIVE = 'components/chat/providers/'

/**
 * Where a provider's own wire vocabulary may appear.
 *
 * The plugin layer, and nothing else outside the generated output. Each provider's
 * tokens live in the directory that reads them -- `acp/updateVocabulary.ts`,
 * `codex/itemVocabulary.ts`, `claude/toolNames.ts` -- as named tables, so a call
 * site reads a constant instead of retyping a string. `generated/` is emitted from
 * the contracts, which are the source of truth for every token that crosses a
 * language boundary.
 */
const WIRE_TOKEN_ALLOWED_PREFIXES = [
  PROVIDERS_RELATIVE,
  'generated/',
]

/**
 * The display surfaces that MAY identify one provider.
 *
 * `AgentProviderIcon` is the whole list: an icon is a per-provider asset, and no
 * shared shape can supply one. A DEFAULT value needs no entry here: the rule below
 * matches a comparison, a provider-keyed table and a read at one member, and
 * `?? AgentProvider.CLAUDE_CODE` is none of the three.
 */
const PROVIDER_COMPARISON_ALLOWED = new Set([
  'components/common/AgentProviderIcon.tsx',
])

/**
 * Every source module outside the plugin layer.
 *
 * Two exclusions beyond the plugin layer and the generated output, and both are
 * deliberate. A TEST builds one fixture per provider and has to list each of them
 * to do it, which is the opposite of the drift these rules exist to prevent. And
 * `test-support/` holds the fixture CORPORA -- `savedDecisionCorpus.ts` is bytes
 * captured verbatim from the installed runtimes, and a corpus that paraphrased a
 * provider's own words would stop being one.
 */
function sharedModules(): string[] {
  return collectFiles(SRC_DIR, {
    matches: name => (name.endsWith('.ts') || name.endsWith('.tsx'))
      && !name.endsWith('.test.ts')
      && !name.endsWith('.test.tsx')
      && !name.endsWith('.d.ts'),
  }).filter((file) => {
    const relative = posixRelative(SRC_DIR, file)
    return !relative.startsWith(PROVIDERS_RELATIVE)
      && !relative.startsWith('generated/')
      && !relative.startsWith('test-support/')
  })
}

/**
 * Every form that decides by provider.
 *
 * A comparison is the obvious one, and it was the only one this rule matched. The two
 * beside it decide just as completely, and they are what a developer reaches for the
 * moment the comparison form fails the suite:
 *
 *   - a TOTAL table over the providers -- `Record<AgentProvider, T>`, which is a second
 *     registry beside the plugin one and holds one hand-written entry for each member;
 *   - one member as a key or an index -- `{ [AgentProvider.CODEX]: ... }`, `TABLE[AgentProvider.CODEX]`.
 *
 * Two forms are deliberately absent, because neither decides anything. A DEFAULT value
 * states a fallback: `agentProvider ?? AgentProvider.CLAUDE_CODE`, which three shared
 * modules hold. And a `Map<AgentProvider, T>` keyed at runtime serves every provider
 * the same way -- `settingsLabelCache` caches option labels under the provider it read
 * them from, and spells no member at all.
 */
const PROVIDER_COMPARISON_RE = new RegExp([
  // `provider === AgentProvider.CODEX`, `case AgentProvider.CODEX:`
  String.raw`(?:===|!==|\bcase)\s*AgentProvider\.[A-Z_]+`,
  // `AgentProvider.CODEX === provider`
  String.raw`AgentProvider\.[A-Z_]+\s*(?:===|!==)`,
  // A hand-written entry for every provider, which drifts the moment one is added.
  String.raw`Record<\s*AgentProvider\s*,`,
  // A computed key at one member: `{ [AgentProvider.CODEX]: 'x' }`.
  String.raw`\[AgentProvider\.[A-Z_]+\]\s*:`,
  // An index read at one member: `TABLE[AgentProvider.CODEX]`. The leading character
  // is what separates it from the ARRAY literal `[AgentProvider.CODEX]`, which lists
  // a provider rather than deciding by one.
  String.raw`[\w)\]]\[AgentProvider\.[A-Z_]+\]`,
].join('|'))

/**
 * The wire words that belong to ONE provider and to no shared vocabulary.
 *
 * Curated rather than exhaustive, and every entry earns its place by being
 * unambiguous. `tool_use` and `tool_result` are deliberately ABSENT: LeapMux's own
 * `MessageCategory` spells its kinds with the same two words, so a rule that
 * matched them would report every classifier in the chat view and teach the next
 * reader to widen the allowlist instead of moving the code.
 */
const WIRE_TOKEN_PATTERNS = [
  // Namespaced JSON-RPC methods. Each namespace belongs to one runtime.
  /['"`](?:cursor|_goose|mcp)\/[a-z_/]+['"`]/,
  /['"`]_reasonix\.io\/[a-z_/]+['"`]/,
  /['"`]session\/(?:update|request_permission|new|prompt|load)['"`]/,
  /['"`]interaction\/requestUserInput['"`]/,
  // Agent Client Protocol session-update discriminators.
  /['"`](?:tool_call_update|agent_message_chunk|agent_thought_chunk|available_commands_update|session_info_update|config_option_update)['"`]/,
  // Codex item types.
  /['"`](?:commandExecution|fileChange|mcpToolCall|dynamicToolCall|collabAgentToolCall)['"`]/,
  // Pi event types.
  /['"`](?:entry_appended|tool_execution_start|tool_execution_end|agent_settled|compaction_start|compaction_end)['"`]/,
  // Claude envelope discriminators that name no shared concept.
  /['"`](?:tool_use_result|compact_boundary)['"`]/,
]

describe('a provider vocabulary stays inside its plugin', () => {
  it('finds the modules it guards', () => {
    expect(sharedModules().length).toBeGreaterThan(100)
  })

  it('decides by no single provider outside the plugin layer', () => {
    const offences: string[] = []
    for (const file of sharedModules()) {
      const relative = posixRelative(SRC_DIR, file)
      if (PROVIDER_COMPARISON_ALLOWED.has(relative))
        continue
      const source = stripCommentLines(readFileSync(file, 'utf8'))
      const match = PROVIDER_COMPARISON_RE.exec(source)
      if (match)
        offences.push(`${relative}:${lineNumberAt(source, match.index)}`)
    }
    expect(
      offences,
      'Shared code must not decide by provider. Add a method to the `Provider` plugin '
      + 'interface in `components/chat/providers/registry.ts` and let each plugin answer it.',
    ).toEqual([])
  })

  // The rule matched a comparison alone for a long time, and a per-provider TABLE went
  // straight past it -- which is the first refactor a developer reaches for once the
  // comparison form fails the suite. These samples pin each form the rule now reads,
  // and pin the three it must keep reading as neutral.
  it('reads every form that decides by provider, and no neutral one', () => {
    const decides = [
      'if (message.agentProvider === AgentProvider.CODEX)',
      'if (AgentProvider.CODEX !== message.agentProvider)',
      '    case AgentProvider.PI:',
      'const WORDS: Record<AgentProvider, string> = fill()',
      'const WORDS = { [AgentProvider.CODEX]: \'Codex\' }',
      'const word = WORDS[AgentProvider.CODEX]',
    ]
    const neutral = [
      // A fallback, which states no decision.
      'const provider = props.agent?.agentProvider ?? AgentProvider.CLAUDE_CODE',
      // A cache keyed at runtime, which serves every provider the same way.
      'const cache = new Map<AgentProvider, string>()',
      // A LIST of providers, which decides nothing about the one in hand.
      'const seeded = [AgentProvider.CLAUDE_CODE]',
    ]
    expect(decides.filter(line => !PROVIDER_COMPARISON_RE.test(line)), 'the rule reads none of these').toEqual([])
    expect(neutral.filter(line => PROVIDER_COMPARISON_RE.test(line)), 'the rule reports these wrongly').toEqual([])
  })

  it('spells no provider wire token outside the plugin layer', () => {
    const offences: string[] = []
    for (const file of sharedModules()) {
      const relative = posixRelative(SRC_DIR, file)
      if (WIRE_TOKEN_ALLOWED_PREFIXES.some(prefix => relative.startsWith(prefix)))
        continue
      const source = stripCommentLines(readFileSync(file, 'utf8'))
      for (const pattern of WIRE_TOKEN_PATTERNS) {
        const match = pattern.exec(source)
        if (match)
          offences.push(`${relative}:${lineNumberAt(source, match.index)} ${match[0]}`)
      }
    }
    expect(
      offences,
      'A provider wire word belongs in a named table inside that provider plugin, '
      + 'which a call site reads as a constant.',
    ).toEqual([])
  })

  // The allowlist must stay pinned to files that exist, for the same reason the
  // drawing allowlist above does.
  it('keeps every allowed comparison site pinned to a file that exists', () => {
    const present = new Set(sharedModules().map(file => posixRelative(SRC_DIR, file)))
    const stale = [...PROVIDER_COMPARISON_ALLOWED].filter(entry => !present.has(entry))
    expect(stale, 'Delete the entry, or repoint it at the module that replaced it.').toEqual([])
  })
})
