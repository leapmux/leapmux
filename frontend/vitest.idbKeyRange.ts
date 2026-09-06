// ---------------------------------------------------------------------------
// Install `globalThis.IDBKeyRange` BEFORE anything imports dexie.
//
// A SETUP FILE OF ITS OWN, AND THAT IS THE WHOLE POINT. Dexie runs
// `Dexie.maxKey = getMaxKey(Dexie.dependencies.IDBKeyRange)` at MODULE
// EVALUATION, and `getMaxKey` is a SELF-REPLACING closure: with no global
// IDBKeyRange it throws inside its own `try` and permanently rebinds itself to
// a string fallback. Every later call gets that fallback -- including the Dexie
// constructor's own `this._maxKey` and DBCore's `MAX_KEY` -- so passing
// `IDBKeyRange` in the constructor options does NOT undo it, and the suite
// would pad compound-index prefix ranges as `'￿'` where a browser uses
// `[[]]`.
//
// `vitest.setup.ts` cannot do this itself. It imports `~/lib/browserStorage`,
// which reaches dexie through `browserStorageDb` -> `idb`, and ES modules
// evaluate every import before the importing module's own body -- so an
// assignment written in that file, at any position, runs too late. Hence a
// separate module, listed FIRST in `setupFiles`. Keep it first, and keep it
// free of app imports.
//
// The `indexedDB` FACTORY is deliberately NOT installed here. See
// `vitest.setup.ts` for why each test that needs one stubs its own.
// `src/lib/idb.test.ts` pins the outcome this file exists for.
// ---------------------------------------------------------------------------
import { IDBKeyRange as FakeIDBKeyRange } from 'fake-indexeddb'

globalThis.IDBKeyRange ??= FakeIDBKeyRange as unknown as typeof globalThis.IDBKeyRange
