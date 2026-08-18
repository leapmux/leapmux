/**
 * JSON.parse guarded for the empty string and malformed documents.
 *
 * Both settings tiers read the same wire shape: `SettingValue.valueJson` and
 * `SettingValue.effectiveJson` are plain JSON strings, and an absent value
 * arrives as `''`. The guard lives here, above both consumers, so the
 * account tier (`~/context/PreferencesContext`) and the hub tier
 * (`~/components/settings/protoRegistry`) cannot answer a malformed
 * document differently.
 */
export function parseSettingJson(json: string): unknown {
  if (json === '')
    return undefined
  try {
    return JSON.parse(json)
  }
  catch {
    return undefined
  }
}
