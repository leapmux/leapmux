import antfu from '@antfu/eslint-config'
import chatPipelinePlugin from './eslint/chatPipelinePlugin'

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
  files: ['src/**/*.ts', 'src/**/*.tsx'],
  ignores: ['src/**/*.test.ts', 'src/**/*.test.tsx', 'src/test-support/**', 'src/generated/**'],
  plugins: {
    'chat-pipeline': chatPipelinePlugin,
  },
  languageOptions: {
    parserOptions: {
      projectService: {
        allowDefaultProject: [
          'src/components/chat/auditProbe.ts',
          'src/components/chat/model/auditProbe.ts',
          'src/components/chat/providers/auditProbe.ts',
          'src/components/chat/providers/probe/plugin.ts',
          'src/components/chat/results/auditProbe.ts',
        ],
      },
      tsconfigRootDir: import.meta.dirname,
    },
  },
  rules: {
    'chat-pipeline/layer-imports': 'error',
    'chat-pipeline/no-provider-decision': 'error',
    'chat-pipeline/no-forbidden-assertion': 'error',
    'chat-pipeline/plugin-registration-only': 'error',
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
})
