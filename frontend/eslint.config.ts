import antfu from '@antfu/eslint-config'

/**
 * The selectors that antfu's TypeScript config puts in `no-restricted-syntax`.
 *
 * ESLint REPLACES the options of a rule; it never merges them. A later config
 * object that sets `no-restricted-syntax` for `src/**` therefore deletes these
 * two selectors in exactly the tree that ships. Each block below that sets the
 * rule must spread this constant first.
 *
 * `src/test-support/restrictedSyntaxKeepsBaseRules.test.ts` resolves the real
 * config for a `src/` file and fails the suite if a selector disappears.
 *
 * - `TSEnumDeclaration[const=true]`: a `const enum` disappears at compile time,
 *   so Vite's per-file transform cannot inline its members.
 * - `TSExportAssignment`: `export =` is a CommonJS form that an ES module
 *   cannot import.
 */
const ANTFU_RESTRICTED_SYNTAX = ['TSEnumDeclaration[const=true]', 'TSExportAssignment'] as const

/**
 * The DOM-`title` ban, factored so every scoped block that sets
 * `no-restricted-syntax` can spread it beside the antfu selectors above.
 *
 * A bare `title` on a DOM element renders the unthemed OS tooltip and silently
 * becomes the element's accessible name when no `aria-label` sits beside it.
 */
const DOM_TITLE_RESTRICTED_SYNTAX = {
  selector: 'JSXOpeningElement[name.type="JSXIdentifier"][name.name=/^[a-z]/] > JSXAttribute[name.name="title"]',
  message: 'Do not put `title` on a DOM element: it renders the unthemed OS tooltip, and it silently becomes the element\'s accessible name. Wrap the element in <Tooltip text={...}> instead -- it works on a disabled control too.',
} as const

/**
 * Every `no-restricted-syntax` option list in this file starts from these.
 * ESLint replaces rule options rather than merging them, so a scoped block
 * that omits one of these deletes it for exactly the tree it matches.
 */
const BASE_RESTRICTED_SYNTAX = [...ANTFU_RESTRICTED_SYNTAX, DOM_TITLE_RESTRICTED_SYNTAX]

/**
 * The Dexie import ban the storage block puts on all of `src/`, factored for
 * the same reason: the chat layer blocks below restate `ts/no-restricted-imports`
 * for their trees and must keep it.
 */
const BASE_RESTRICTED_IMPORT_PATHS = [{
  name: 'dexie',
  message: 'Open IndexedDB through ~/lib/idb (createIdbConnection).',
  allowTypeImports: true,
}] as const

/**
 * Selectors that keep shared code provider-neutral: no decision by one
 * `AgentProvider`, and none of the curated provider wire literals.
 *
 * The forms a decision takes: a comparison, a `case`, a computed key, an index
 * read at one member, and a total `Record<AgentProvider, T>` table. Deliberately
 * NOT matched: a fallback (`provider ?? AgentProvider.CLAUDE_CODE`), a runtime
 * `Map<AgentProvider, T>` cache, and a list of providers -- none of them decides
 * anything about the provider in hand.
 *
 * `src/components/common/AgentProviderIcon.tsx` is the one display surface that
 * may identify a provider (an icon is a per-provider asset); its block below
 * lifts the decision selectors alone.
 */
const PROVIDER_NEUTRALITY_SYNTAX = [
  {
    selector: 'BinaryExpression[operator=/^[!=]==$/][left.type=\'MemberExpression\'][left.object.name=\'AgentProvider\']',
    message: 'Shared code must not decide by provider. Add a method to the `Provider` plugin interface in `components/chat/providers/registry.ts` and let each plugin answer it.',
  },
  {
    selector: 'BinaryExpression[operator=/^[!=]==$/][right.type=\'MemberExpression\'][right.object.name=\'AgentProvider\']',
    message: 'Shared code must not decide by provider. Add a method to the `Provider` plugin interface in `components/chat/providers/registry.ts` and let each plugin answer it.',
  },
  {
    selector: 'SwitchCase[test.type=\'MemberExpression\'][test.object.name=\'AgentProvider\']',
    message: 'Shared code must not decide by provider. Add a method to the `Provider` plugin interface in `components/chat/providers/registry.ts` and let each plugin answer it.',
  },
  {
    selector: 'Property[computed=true][key.type=\'MemberExpression\'][key.object.name=\'AgentProvider\']',
    message: 'A hand-written entry for one provider drifts the moment another is added. Fill the provider plugin instead.',
  },
  {
    selector: 'MemberExpression[computed=true][property.type=\'MemberExpression\'][property.object.name=\'AgentProvider\']',
    message: 'A shared module reading one member of a provider-keyed table is deciding by provider. Fill the provider plugin instead.',
  },
  {
    selector: 'TSTypeReference[typeName.name=\'Record\'] TSTypeReference[typeName.name=\'AgentProvider\']',
    message: 'A total `Record<AgentProvider, T>` is a second registry beside the plugin one and holds one hand-written entry per provider. Fill the provider plugin instead.',
  },
] as const

/**
 * The wire words that belong to ONE provider and to no shared vocabulary.
 *
 * Curated rather than exhaustive, and every entry earns its place by being
 * unambiguous. `tool_use` and `tool_result` are deliberately ABSENT:
 * LeapMux's own `MessageCategory` spells its kinds with the same two words, so
 * a rule that matched them would report every classifier in the chat view.
 * Each pattern is anchored to the WHOLE literal, which is what the quoted form
 * of the same rule stated: a token, not a substring of a longer sentence.
 */
const WIRE_TOKEN_REGEXPS = [
  String.raw`^(?:cursor|_goose|mcp)\/[a-z_/]+$`,
  String.raw`^_reasonix\.io\/[a-z_/]+$`,
  String.raw`^session\/(?:update|request_permission|new|prompt|load)$`,
  String.raw`^interaction\/requestUserInput$`,
  String.raw`^(?:tool_call_update|agent_message_chunk|agent_thought_chunk|available_commands_update|session_info_update|config_option_update)$`,
  String.raw`^(?:commandExecution|fileChange|mcpToolCall|dynamicToolCall|collabAgentToolCall)$`,
  String.raw`^(?:entry_appended|tool_execution_start|tool_execution_end|agent_settled|compaction_start|compaction_end)$`,
  String.raw`^(?:tool_use_result|compact_boundary)$`,
] as const

/** The wire-token selectors, over string literals and template elements alike. */
const WIRE_TOKEN_SYNTAX = WIRE_TOKEN_REGEXPS.flatMap(pattern => [
  { selector: `Literal[value=/${pattern}/]`, message: 'A provider wire word belongs in a named table inside that provider plugin, which a call site reads as a constant.' },
  { selector: `TemplateElement[value.cooked=/${pattern}/]`, message: 'A provider wire word belongs in a named table inside that provider plugin, which a call site reads as a constant.' },
]) as const

/**
 * The assertions that re-pair a `ToolKind` with a request, a payload or a
 * result the kind does not declare. The renderers read those fields without a
 * guard, so the row does not draw wrong -- it throws, and the error boundary
 * replaces the whole message.
 *
 * `ir/toolCall.ts` is the one exemption: `buildToolCall` checks the lifecycle
 * rules at runtime, over a draft whose status came from the wire, and no
 * narrowing carries a runtime answer back into the type system. The assertion
 * there stands on the check immediately above it.
 */
const TYPE_ASSERTION_NODES = ['TSAsExpression', 'TSTypeAssertion'] as const
const TOOL_CALL_TYPE_NAMES = '^(ToolCallIR|ToolCallPayloadIR|ToolCallPayloadOf|ToolCallPayload|ToolCallForKind|ToolCallPayloadForKind|ToolCallOf|ToolCallOfKinds|ToolRequests|ToolResults|ToolResultOf|ParsedCall|ResolvedCall)$'

const TOOL_CALL_ASSERTION_SYNTAX = TYPE_ASSERTION_NODES.flatMap(node => [
  {
    selector: `${node} TSTypeReference[typeName.name=/${TOOL_CALL_TYPE_NAMES}/]`,
    message: 'An assertion that contains a tool-call type re-pairs a kind with a request or a result the kind does not declare. Build the payload at a literal kind and hand it to `toolCall`.',
  },
  {
    selector: `${node} TSTypeReference[typeName.right.name=/${TOOL_CALL_TYPE_NAMES}/]`,
    message: 'An assertion that contains a qualified tool-call type re-pairs a kind with an unrelated payload. Build the payload at a literal kind and hand it to `toolCall`.',
  },
  {
    selector: `${node} TSImportType[qualifier.name=/${TOOL_CALL_TYPE_NAMES}/]`,
    message: 'An assertion through an imported tool-call type re-pairs a kind with an unrelated payload. Import the type normally and build the payload at a literal kind.',
  },
  {
    selector: `${node} TSImportType[qualifier.right.name=/${TOOL_CALL_TYPE_NAMES}/]`,
    message: 'An assertion through a qualified imported tool-call type re-pairs a kind with an unrelated payload. Import the type normally and build the payload at a literal kind.',
  },
])

/** Only the provider registry can apply the brand after it resolves a message. */
const RESOLVED_CONTENT_ASSERTION_SYNTAX = TYPE_ASSERTION_NODES.flatMap(node => [
  {
    selector: `${node} TSTypeReference[typeName.name='ResolvedMessageContent']`,
    message: 'Only `providers/registry.ts` can assert a type that contains `ResolvedMessageContent`. Call `resolveMessageForRendering`, then pass its result through the pipeline.',
  },
  {
    selector: `${node} TSTypeReference[typeName.right.name='ResolvedMessageContent']`,
    message: 'Only `providers/registry.ts` can assert a type that contains qualified `ResolvedMessageContent`. Call `resolveMessageForRendering`, then pass its result through the pipeline.',
  },
  {
    selector: `${node} TSImportType[qualifier.name='ResolvedMessageContent']`,
    message: 'Only `providers/registry.ts` can assert imported `ResolvedMessageContent`. Call `resolveMessageForRendering`, then pass its result through the pipeline.',
  },
  {
    selector: `${node} TSImportType[qualifier.right.name='ResolvedMessageContent']`,
    message: 'Only `providers/registry.ts` can assert qualified imported `ResolvedMessageContent`. Call `resolveMessageForRendering`, then pass its result through the pipeline.',
  },
])

interface RestrictedModuleSyntaxOptions {
  includeImportType?: boolean
}

/** Cover module edges that `ts/no-restricted-imports` does not inspect. */
function restrictedModuleSyntax(pattern: string, message: string, options: RestrictedModuleSyntaxOptions = {}) {
  const selectors = [
    `ImportExpression[source.value=/${pattern}/]`,
    `CallExpression[callee.type='Identifier'][callee.name='require'][arguments.0.value=/${pattern}/]`,
    `TSImportEqualsDeclaration[moduleReference.expression.value=/${pattern}/]`,
  ]
  if (options.includeImportType)
    selectors.push(`TSImportType[source.value=/${pattern}/]`)
  return selectors.map(selector => ({ selector, message }))
}

const COMPUTED_MODULE_SYNTAX = [
  {
    selector: 'ImportExpression:not([source.type=\'Literal\'])',
    message: 'A computed dynamic import can hide a dependency across chat layers. Use a string literal so the architecture rule can inspect it.',
  },
  {
    selector: 'CallExpression[callee.type=\'Identifier\'][callee.name=\'require\']:not([arguments.0.type=\'Literal\'])',
    message: 'A computed require call can hide a dependency across chat layers. Use a string literal so the architecture rule can inspect it.',
  },
] as const

const IR_FORBIDDEN_MODULE_PATTERN = String.raw`(?:^|\/)(?:components|providers|results|stores)(?:\/|$)|^lucide-solid(?:\/|$)|\.(?:tsx|css|css\.ts)$`
const IR_FORBIDDEN_MODULE_SYNTAX = restrictedModuleSyntax(
  IR_FORBIDDEN_MODULE_PATTERN,
  'The IR is layer 2. It cannot load a provider, renderer, store, stylesheet, component, or icon library. Move a neutral shape into `~/models/`.',
  { includeImportType: true },
)
const IR_ALIAS_VALUE_IMPORT_SYNTAX = restrictedModuleSyntax(
  String.raw`^~\/(?!lib\/|generated\/|models\/)`,
  'A value import into `ir/` comes from `~/lib`, `~/generated`, `~/models` or a module `ir/` owns. Move the pure helper beside the type it serves, or make the import type-only.',
)
const IR_ROOT_RELATIVE_VALUE_IMPORT_SYNTAX = restrictedModuleSyntax(
  String.raw`^\.\.\/(?!diff\/(diffBuilder|diffTypes|unifiedDiffParser)$)`,
  'A value import from `ir/` may reach the three pure diff modules and nothing else above the directory.',
)
const IR_TOOL_RELATIVE_VALUE_IMPORT_SYNTAX = restrictedModuleSyntax(
  String.raw`^\.\.\/(?![^/]+$|\.\.\/diff\/(diffBuilder|diffTypes|unifiedDiffParser)$)`,
  'From `ir/tools/`, a value import may climb to a sibling in `ir/` or to the three pure diff modules. Anything else leaves the layer.',
)
const PROVIDER_RESULT_IMPORT_SYNTAX = restrictedModuleSyntax(
  String.raw`(?:^|\/)results(?:\/|$)`,
  'A plugin reads its provider bytes into the IR. Move the pure helper into `components/chat/ir/` beside the type it builds.',
  { includeImportType: true },
)
const RESULT_PROVIDER_IMPORT_SYNTAX = restrictedModuleSyntax(
  String.raw`(?:^|\/)providers(?:\/|$)`,
  'A renderer draws the row IR. It cannot load a provider plugin. Move the shared shape into `components/chat/ir/`.',
  { includeImportType: true },
)

/** The chat pipeline's implementation trees: never a test, a fixture or a harness. */
const CHAT_IMPLEMENTATION_IGNORES = [
  '**/*.test.ts',
  '**/*.test.tsx',
  '**/*.fixtures.ts',
  '**/testUtils.ts',
  '**/testUtils.tsx',
  '**/testMocks.ts',
] as const

export default antfu({
  stylistic: {
    indent: 2,
    quotes: 'single',
  },
  solid: true,
  ignores: ['src/gen/**', '.vinxi/**', '.output/**', 'app.config.timestamp_*'],
}, {
  // Treat `useDialogSubmit`'s returned helpers as reactive entry points.
  // `run` and `formHandler` invoke their callback synchronously inside an
  // event-handler call stack, so a body that captures reactive props is
  // safe — the body reads the captures before any subsequent prop update. The
  // plugin already auto-detects `create*` / `use*` names; this option
  // extends that allowlist to the helpers returned from useDialogSubmit.
  //
  // The `files` filter mirrors antfu's solid config (JSX/TSX only) so we
  // don't widen the rule's surface area to `.ts` files where it did not run
  // before.
  files: ['**/*.jsx', '**/*.tsx'],
  rules: {
    'solid/reactivity': ['warn', {
      customReactiveFunctions: ['run', 'formHandler'],
    }],
  },
}, {
  // Every browser-storage access goes through `~/lib/browserStorage`, which
  // composes the account-scoped key and the `{ v, e }` TTL envelope. A direct
  // call skips both: a second account on the browser can read the value, and
  // the next page-load sweep deletes it, so the feature works once and then
  // silently forgets its state on every reload.
  //
  // At the AST level rather than by text, because the class is "a reference to
  // the global", not one spelling of it: `localStorage['k'] = v` and
  // `const s = sessionStorage` write the same broken entry.
  // `src/test-support/storageKeysAreRegistered.test.ts` is the guard, and it
  // also holds the two registry rules that have no lint equivalent.
  //
  // `indexedDB` is confined the same way, one layer down: a raw `indexedDB.open`
  // skips the schema check that deletes and rebuilds a drifted database, so the
  // store opens, is the wrong shape, and fails at the first cursor instead.
  //
  // THE THREE STORAGE MODULES ARE EXEMPTED PER RULE, NOT BY `ignores`. An
  // `ignores` at this level removes a file from the WHOLE config object, so
  // listing `browserStorageDb.ts` and `idb.ts` there also lifted the `dexie`
  // import ban off them -- and the ban exists to keep `new Dexie(...)` in the
  // one module that pairs it with the shape check. Each module now loses only
  // the rule it must: the gateway pair may name the raw globals, and `~/lib/idb`
  // may also import Dexie.
  //
  // Tests and E2E specs are exempt. A unit test drives the gateway's own
  // behaviour, and an E2E `page.evaluate` body runs in the browser, where the
  // module does not exist.
  files: ['src/**/*.ts', 'src/**/*.tsx'],
  ignores: ['src/**/*.test.ts', 'src/**/*.test.tsx'],
  rules: {
    'no-restricted-globals': ['error', { name: 'localStorage', message: 'Route browser storage through ~/lib/browserStorage.' }, { name: 'sessionStorage', message: 'Route browser storage through ~/lib/browserStorage.' }, { name: 'indexedDB', message: 'Open IndexedDB through ~/lib/idb (createIdbConnection).' }],
    'no-restricted-properties': ['error', { object: 'window', property: 'localStorage', message: 'Route browser storage through ~/lib/browserStorage.' }, { object: 'window', property: 'sessionStorage', message: 'Route browser storage through ~/lib/browserStorage.' }, { object: 'window', property: 'indexedDB', message: 'Open IndexedDB through ~/lib/idb (createIdbConnection).' }, { object: 'globalThis', property: 'localStorage', message: 'Route browser storage through ~/lib/browserStorage.' }, { object: 'globalThis', property: 'sessionStorage', message: 'Route browser storage through ~/lib/browserStorage.' }, { object: 'globalThis', property: 'indexedDB', message: 'Open IndexedDB through ~/lib/idb (createIdbConnection).' }],
    // Every store declares a schema and takes a connection from the scaffold;
    // nobody else constructs a Dexie. This is the import-level statement of the
    // rule `no-restricted-globals` makes for the raw API above.
    //
    // TYPE imports stay allowed, and that is the point of using the
    // TypeScript-aware rule: a store still has to name `Table<Row>` to type the
    // tables its connection hands back, and a type cannot open a database.
    'ts/no-restricted-imports': ['error', {
      paths: [...BASE_RESTRICTED_IMPORT_PATHS],
    }],
  },
}, {
  // The gateway pair IS the browser-storage layer, so it spells the raw globals
  // the rule above confines. It still may not construct a Dexie: that is
  // `~/lib/idb`'s job, and only there is an open paired with the shape check.
  files: ['src/lib/browserStorage.ts', 'src/lib/browserStorageDb.ts'],
  rules: {
    'no-restricted-globals': 'off',
    'no-restricted-properties': 'off',
  },
}, {
  // `~/lib/idb` is where Dexie is constructed, so it is the one module that may
  // import it -- and it wraps the raw `indexedDB` global, so it spells that too.
  files: ['src/lib/idb.ts'],
  rules: {
    'no-restricted-globals': 'off',
    'no-restricted-properties': 'off',
    'ts/no-restricted-imports': 'off',
  },
}, {
  // `title` on a DOM element is banned. Use `<Tooltip>` (or a component that
  // routes its own `title` prop through one, as `IconButton` does).
  //
  // Two reasons, and the second one causes harm with no visible sign. A native
  // `title` renders the OS tooltip, which ignores the app's theme and
  // typography, waits a browser-controlled delay, and never appears on touch.
  // And on a control with no `aria-label`, a `title` long enough to state a
  // reason BECOMES the accessible name: a screen reader then announces three
  // sentences of remedy where "Add passkey" belongs, and every by-name lookup
  // stops matching. Nothing in the type system catches it, and it renders
  // fine, so it survives review.
  //
  // The carve-out this replaced was "a DISABLED control may use `title`,
  // because it takes no pointer events and `<Tooltip>` cannot fire on it".
  // `<Tooltip>` covers that case now: it gives its wrapper a box, listens
  // there, and leaves an offscreen description in `aria-describedby` for as
  // long as the control is disabled.
  //
  // A LOWERCASE element name only. `title` on a component is that component's
  // own prop -- `<Dialog title>` is a heading, `<IconButton title>` is a
  // tooltip -- and the selector cannot know which. A component that SPREADS
  // its props onto a DOM node closes that hole in the type system instead, by
  // omitting `title` from its prop type; `IconButton` and `ConfirmButton` both
  // do.
  files: ['src/**/*.ts', 'src/**/*.tsx', 'tests/**/*.ts', 'tests/**/*.tsx'],
  rules: {
    'no-restricted-syntax': ['error', ...BASE_RESTRICTED_SYNTAX],
  },
}, {
  // A `describe` identifies the SYMBOL under test, so it must be free to spell that
  // symbol: `describe('DirectoryTree')`, `describe('MESSAGE_UI_DEFAULTS')`. The
  // rule rejects any title opening with a capital, and its `--fix` lowercases
  // character 0 alone -- so a name that keeps its capital came back misspelled
  // (`DEFAULT_MONO_FONT_FAMILY` -> `dEFAULT_MONO_FONT_FAMILY`). Three hundred
  // titles worked around it instead, either by flattening the name to
  // `directorytree` or by dropping its leading capital to `directoryTree`, and
  // both spell an identifier that does not exist.
  //
  // `it` and `test` keep the rule, because those titles are SENTENCES that
  // continue the word `it`: `it('returns null for an empty payload')`. A capital
  // there is Title Case prose, which is what this rule is for.
  //
  // `src/test-support/noMangledTestTitles.test.ts` carries the other half: a
  // title may not spell a name the file knows with its capitals removed.
  files: ['**/*.test.ts', '**/*.test.tsx', '**/*.spec.ts', '**/*.spec.tsx'],
  rules: {
    'test/prefer-lowercase-title': ['error', { ignore: ['describe'] }],
  },
}, {
  // Playwright fixture parameters (e.g. `authenticatedWorkspace`) must be destructured
  // to activate the fixture, even when the test body does not use them directly.
  files: ['tests/e2e/**/*.spec.ts'],
  rules: {
    'unused-imports/no-unused-vars': ['error', {
      argsIgnorePattern: '^(authenticatedWorkspace|workspace|leapmuxServer|separateHubWorker)$',
    }],
  },
}, {
  // Shared code stays provider-neutral. The plugin layer and the generated
  // contracts own the provider vocabulary; everything else decides through the
  // `Provider` interface. Tests are exempt because a unit test builds one
  // fixture per provider, and `test-support/` holds corpus bytes captured
  // verbatim from the installed runtimes.
  files: ['src/**/*.ts', 'src/**/*.tsx'],
  ignores: ['src/components/chat/providers/**', 'src/generated/**', 'src/test-support/**', 'src/**/*.test.ts', 'src/**/*.test.tsx', 'src/**/*.d.ts'],
  rules: {
    'no-restricted-syntax': ['error', ...BASE_RESTRICTED_SYNTAX, ...PROVIDER_NEUTRALITY_SYNTAX, ...WIRE_TOKEN_SYNTAX, ...RESOLVED_CONTENT_ASSERTION_SYNTAX],
  },
}, {
  // The provider-icon exception: an icon is a per-provider asset and no shared
  // shape can supply one, so this display surface may identify a provider. It
  // keeps the wire-token selectors; only the decision selectors lift.
  files: ['src/components/common/AgentProviderIcon.tsx'],
  rules: {
    'no-restricted-syntax': ['error', ...BASE_RESTRICTED_SYNTAX, ...WIRE_TOKEN_SYNTAX, ...RESOLVED_CONTENT_ASSERTION_SYNTAX],
  },
}, {
  // No producer pairs a `ToolKind` with another kind's request, payload or
  // result by assertion; `ir/toolCall.ts` (the checked builder) is exempted
  // further below. The plugin layer joins this rule through its own block,
  // which carries the assertion selectors alone.
  files: ['src/components/chat/**/*.ts', 'src/components/chat/**/*.tsx'],
  ignores: [...CHAT_IMPLEMENTATION_IGNORES, '**/*.css.ts', 'src/components/chat/providers/**'],
  rules: {
    'no-restricted-syntax': ['error', ...BASE_RESTRICTED_SYNTAX, ...PROVIDER_NEUTRALITY_SYNTAX, ...WIRE_TOKEN_SYNTAX, ...TOOL_CALL_ASSERTION_SYNTAX, ...RESOLVED_CONTENT_ASSERTION_SYNTAX],
  },
}, {
  // Layer 2 of the chat render pipeline: `ir/` describes a row and never draws
  // one, so it may not reach a provider, a renderer, a store, a stylesheet, a
  // component or an icon library in ANY form -- a type from one of them makes a
  // render-layer decision part of what a row MEANS.
  files: ['src/components/chat/ir/**/*.ts', 'src/components/chat/ir/**/*.tsx'],
  ignores: CHAT_IMPLEMENTATION_IGNORES,
  rules: {
    'ts/no-restricted-imports': ['error', {
      paths: [
        ...BASE_RESTRICTED_IMPORT_PATHS,
        { name: 'lucide-solid', message: 'The IR states what a row means; which glyph draws it is a render-layer decision. Declare a `ToolIconHint` and map it onto the icon in `results/`.' },
      ],
      patterns: [{
        group: ['~/components/**', '~/stores/**', '**/providers/**', '../providers', '../results', '../results/**', '../../results', '../../results/**', 'lucide-solid/**', '**/*.tsx', '**/*.css', '**/*.css.ts'],
        message: 'The IR is layer 2: it depends on no provider, renderer, store, stylesheet, component or icon library, not even as a type. A neutral shape both layers share belongs in `~/models/`.',
        allowTypeImports: false,
      }],
    }],
  },
}, {
  // The IR's own escape hatch: a VALUE import may come from `~/lib`, `~/generated`,
  // `~/models`, a module `ir/` itself owns, or the three pure diff modules. The
  // regexes below state the two boundaries that `ts/no-restricted-imports`
  // patterns cannot: which `~/` roots a value import may take, and how far a
  // relative specifier may climb. From `ir/` itself the three diff modules sit
  // one level up; a `type` import (which `importKind` reads) may come from a
  // sibling shape such as `../controls/types`.
  files: ['src/components/chat/ir/**/*.ts', 'src/components/chat/ir/**/*.tsx'],
  ignores: [...CHAT_IMPLEMENTATION_IGNORES, 'src/components/chat/ir/*.ts'],
  rules: {
    'no-restricted-syntax': ['error', ...BASE_RESTRICTED_SYNTAX, ...PROVIDER_NEUTRALITY_SYNTAX, ...WIRE_TOKEN_SYNTAX, ...TOOL_CALL_ASSERTION_SYNTAX, ...RESOLVED_CONTENT_ASSERTION_SYNTAX, ...COMPUTED_MODULE_SYNTAX, ...IR_FORBIDDEN_MODULE_SYNTAX, ...IR_ALIAS_VALUE_IMPORT_SYNTAX, ...IR_TOOL_RELATIVE_VALUE_IMPORT_SYNTAX, {
      selector: 'ImportDeclaration[importKind=\'value\'][source.value=/^~\\/(?!lib\\/|generated\\/|models\\/)/]',
      message: 'A value import into `ir/` comes from `~/lib`, `~/generated`, `~/models` or a module `ir/` owns. Move the pure helper beside the type it serves, or make the import type-only.',
    }, {
      selector: 'ImportDeclaration[importKind=\'value\'][source.value=/^\\.\\.\\/(?![^/]+$|\\.\\.\\/diff\\/(diffBuilder|diffTypes|unifiedDiffParser)$)/]',
      message: 'From `ir/tools/`, a value import may climb to a sibling in `ir/` (one name) or to the three pure diff modules. Anything else leaves the layer.',
    }],
  },
}, {
  // The same boundary one directory up: from `ir/` itself, the three diff
  // modules are `../diff/<name>` and every other `../` specifier leaves `ir/`.
  files: ['src/components/chat/ir/*.ts', 'src/components/chat/ir/*.tsx'],
  ignores: CHAT_IMPLEMENTATION_IGNORES,
  rules: {
    'no-restricted-syntax': ['error', ...BASE_RESTRICTED_SYNTAX, ...PROVIDER_NEUTRALITY_SYNTAX, ...WIRE_TOKEN_SYNTAX, ...TOOL_CALL_ASSERTION_SYNTAX, ...RESOLVED_CONTENT_ASSERTION_SYNTAX, ...COMPUTED_MODULE_SYNTAX, ...IR_FORBIDDEN_MODULE_SYNTAX, ...IR_ALIAS_VALUE_IMPORT_SYNTAX, ...IR_ROOT_RELATIVE_VALUE_IMPORT_SYNTAX, {
      selector: 'ImportDeclaration[importKind=\'value\'][source.value=/^~\\/(?!lib\\/|generated\\/|models\\/)/]',
      message: 'A value import into `ir/` comes from `~/lib`, `~/generated`, `~/models` or a module `ir/` owns. Move the pure helper beside the type it serves, or make the import type-only.',
    }, {
      selector: 'ImportDeclaration[importKind=\'value\'][source.value=/^\\.\\.\\/(?!diff\\/(diffBuilder|diffTypes|unifiedDiffParser)$)/]',
      message: 'A value import from `ir/` may reach the three pure diff modules and nothing else above the directory.',
    }],
  },
}, {
  // The one checked assertion. `buildToolCall` verifies the lifecycle rules at
  // runtime over a draft whose status came from the wire; this file may state
  // the pairing the check earned, and it is the only one that may.
  files: ['src/components/chat/ir/toolCall.ts'],
  rules: {
    'no-restricted-syntax': ['error', ...BASE_RESTRICTED_SYNTAX, ...PROVIDER_NEUTRALITY_SYNTAX, ...WIRE_TOKEN_SYNTAX, ...RESOLVED_CONTENT_ASSERTION_SYNTAX, ...COMPUTED_MODULE_SYNTAX, ...IR_FORBIDDEN_MODULE_SYNTAX, ...IR_ALIAS_VALUE_IMPORT_SYNTAX, ...IR_ROOT_RELATIVE_VALUE_IMPORT_SYNTAX, {
      selector: 'ImportDeclaration[importKind=\'value\'][source.value=/^~\\/(?!lib\\/|generated\\/|models\\/)/]',
      message: 'A value import into `ir/` comes from `~/lib`, `~/generated`, `~/models` or a module `ir/` owns. Move the pure helper beside the type it serves, or make the import type-only.',
    }, {
      selector: 'ImportDeclaration[importKind=\'value\'][source.value=/^\\.\\.\\/(?!diff\\/(diffBuilder|diffTypes|unifiedDiffParser)$)/]',
      message: 'A value import from `ir/` may reach the three pure diff modules and nothing else above the directory.',
    }],
  },
}, {
  // Layer 1 reads its provider's bytes into the IR and never draws a transcript
  // row: JSX belongs to the four control surfaces exempted below, which answer a
  // request rather than render a row. A `.ts` module cannot hold JSX at all,
  // which is why transcript extractors stay `.ts`. The assertion ban reaches
  // here too; the provider vocabulary itself does not.
  files: ['src/components/chat/providers/**/*.ts', 'src/components/chat/providers/**/*.tsx'],
  ignores: CHAT_IMPLEMENTATION_IGNORES,
  rules: {
    'no-restricted-syntax': ['error', ...BASE_RESTRICTED_SYNTAX, ...TOOL_CALL_ASSERTION_SYNTAX, ...RESOLVED_CONTENT_ASSERTION_SYNTAX, ...COMPUTED_MODULE_SYNTAX, ...PROVIDER_RESULT_IMPORT_SYNTAX],
  },
}, {
  // The registry applies the resolved-content brand after it runs the selected
  // provider resolver. It keeps the tool assertion and import restrictions.
  files: ['src/components/chat/providers/registry.ts'],
  rules: {
    'no-restricted-syntax': ['error', ...BASE_RESTRICTED_SYNTAX, ...TOOL_CALL_ASSERTION_SYNTAX, ...COMPUTED_MODULE_SYNTAX, ...PROVIDER_RESULT_IMPORT_SYNTAX],
  },
}, {
  // The JSX half of the same rule, for the `.tsx` modules a plugin may hold.
  files: ['src/components/chat/providers/**/*.tsx'],
  ignores: CHAT_IMPLEMENTATION_IGNORES,
  rules: {
    'no-restricted-syntax': ['error', ...BASE_RESTRICTED_SYNTAX, ...TOOL_CALL_ASSERTION_SYNTAX, ...RESOLVED_CONTENT_ASSERTION_SYNTAX, ...COMPUTED_MODULE_SYNTAX, ...PROVIDER_RESULT_IMPORT_SYNTAX, { selector: 'JSXElement', message: 'A plugin reads its provider\'s bytes into the shared row IR; the shared renderers draw it. Move the markup into `components/chat/results/` and state the call through `ToolCallIR`.' }, { selector: 'JSXFragment', message: 'A plugin reads its provider\'s bytes into the shared row IR; the shared renderers draw it. Move the markup into `components/chat/results/` and state the call through `ToolCallIR`.' }],
  },
}, {
  // The four control surfaces a provider may draw itself: a permission prompt,
  // a question form, a plan approval. They answer a request of that provider;
  // the row IR does not describe them.
  files: [
    'src/components/chat/providers/codex/CodexControlActions.tsx',
    'src/components/chat/providers/cursor/CursorControlActions.tsx',
    'src/components/chat/providers/pi/PiControlActions.tsx',
    'src/components/chat/providers/pi/PiPlanApprovalActions.tsx',
  ],
  rules: {
    'no-restricted-syntax': ['error', ...BASE_RESTRICTED_SYNTAX, ...TOOL_CALL_ASSERTION_SYNTAX, ...RESOLVED_CONTENT_ASSERTION_SYNTAX, ...COMPUTED_MODULE_SYNTAX, ...PROVIDER_RESULT_IMPORT_SYNTAX],
  },
}, {
  // Layer 1 again, on the import edge: a plugin that imports from `results/`
  // puts a parser the whole pipeline depends on behind a module that exists to
  // draw.
  files: ['src/components/chat/providers/**/*.ts', 'src/components/chat/providers/**/*.tsx'],
  ignores: CHAT_IMPLEMENTATION_IGNORES,
  rules: {
    'ts/no-restricted-imports': ['error', {
      paths: [...BASE_RESTRICTED_IMPORT_PATHS],
      patterns: [{
        group: ['**/results/**', '../results', '../../results', '../../../results'],
        message: 'A plugin reads its provider\'s bytes into the IR, and the IR is layer 2. Move the pure helper into `components/chat/ir/` beside the type it builds.',
        allowTypeImports: false,
      }],
    }],
  },
}, {
  // Layer 3, the other end of the same rule: a renderer that imports a plugin
  // reaches for one provider's parser inside the module that exists to draw the
  // SAME row for every provider.
  files: ['src/components/chat/results/**/*.ts', 'src/components/chat/results/**/*.tsx'],
  ignores: [...CHAT_IMPLEMENTATION_IGNORES, '**/*.css.ts'],
  rules: {
    'no-restricted-syntax': ['error', ...BASE_RESTRICTED_SYNTAX, ...PROVIDER_NEUTRALITY_SYNTAX, ...WIRE_TOKEN_SYNTAX, ...TOOL_CALL_ASSERTION_SYNTAX, ...RESOLVED_CONTENT_ASSERTION_SYNTAX, ...COMPUTED_MODULE_SYNTAX, ...RESULT_PROVIDER_IMPORT_SYNTAX],
    'ts/no-restricted-imports': ['error', {
      paths: [...BASE_RESTRICTED_IMPORT_PATHS],
      patterns: [{
        group: ['**/providers/**', '../providers', '../../providers', '../../../providers'],
        message: 'A renderer draws the row IR, and the IR is layer 2. It never reads a plugin: the shape it needs belongs in `components/chat/ir/`, where both layers read it from.',
        allowTypeImports: false,
      }],
    }],
  },
})
