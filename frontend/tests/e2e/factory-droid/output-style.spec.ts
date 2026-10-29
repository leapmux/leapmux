import { droidTest } from '../droid-fixtures'
import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { relatedNativeProof } from './scenarios'

// The stream-jsonrpc session protocol that `droid exec` exposes states no
// output-style axis: neither droid.update_session_settings nor the resolved
// session settings of droid.initialize_session carry one, and no placement of
// the key in the settings files changes a request. The styles are a selector
// of Droid's own interactive UI, stored as a per-session snapshot that no
// protocol request reaches.
droidTest('exposes no separate native output style setting', async ({ native }) => {
  await expectMissingSetting(native, {
    feature: 'output-style',
    relatedProof: () => relatedNativeProof(native),
  })
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/526.
