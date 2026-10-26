//! Tests that pin how Tauri rewrites the Content Security Policy (CSP) of the
//! assets that it serves at `tauri://localhost`.
//!
//! Tauri adds a `'nonce-…'` source to `style-src` whenever a served HTML file
//! holds a `<style>` element, and the built `index.html` holds one: the boot
//! splash CSS. A browser ignores `'unsafe-inline'` in a directive that also
//! holds a nonce or a hash. Every `<style>` element that a script creates then
//! gets no stylesheet, and WebKit logs "Refused to apply a stylesheet because
//! its hash, its nonce, or 'unsafe-inline' does not appear in the style-src
//! directive". These elements need `'unsafe-inline'`:
//!
//! - The Shiki style-class sheet (`frontend/src/lib/shikiStyleClass.ts`), which
//!   gives code blocks and colored tool output their colors
//! - xterm's viewport sheet, and the theme and dimension sheets of xterm's DOM
//!   renderer
//!
//! None of them can carry the nonce. xterm builds its `<style>` elements
//! internally and accepts no nonce, and Tauri picks a new random nonce for each
//! page load. The hub serves the same frontend with `style-src 'self'
//! 'unsafe-inline'` and no nonce (`backend/internal/hub/frontend/csp.go`), so
//! this keeps the two policies equal.

use std::collections::HashMap;

use tauri::utils::config::{Config, Csp, CspDirectiveSources, SecurityConfig};

const BASE_CONFIG: &str = include_str!("../tauri.conf.json");

/// The platform files that Tauri merges over `tauri.conf.json`.
const PLATFORM_CONFIGS: [(&str, &str); 2] = [
    (
        "tauri.linux.conf.json",
        include_str!("../tauri.linux.conf.json"),
    ),
    (
        "tauri.windows.conf.json",
        include_str!("../tauri.windows.conf.json"),
    ),
];

/// The security settings, parsed by Tauri's own config type. `SecurityConfig`
/// rejects an unknown field, so a misspelled key fails here instead of being
/// ignored.
fn security_config() -> SecurityConfig {
    let config: Config =
        serde_json::from_str(BASE_CONFIG).expect("tauri.conf.json must parse as a Tauri config");
    config.app.security
}

/// The sources of one directive, split the way Tauri splits them.
fn directive_sources(csp: &Csp, directive: &str) -> Vec<String> {
    let directives: HashMap<String, CspDirectiveSources> = csp.clone().into();
    directives
        .get(directive)
        .cloned()
        .map(Into::into)
        .unwrap_or_default()
}

/// Each policy that Tauri can serve: `csp` always, and `devCsp` in a dev
/// build when it is set. Tauri falls back to `csp` when `devCsp` is absent.
fn served_policies(security: &SecurityConfig) -> Vec<(&'static str, Csp)> {
    let csp = security
        .csp
        .clone()
        .expect("tauri.conf.json must set app.security.csp");
    let mut policies = vec![("csp", csp)];
    if let Some(dev_csp) = security.dev_csp.clone() {
        policies.push(("devCsp", dev_csp));
    }
    policies
}

#[test]
fn tauri_adds_no_nonce_to_style_src() {
    let security = security_config();
    assert!(
        !security
            .dangerous_disable_asset_csp_modification
            .can_modify("style-src"),
        "Tauri must not add a nonce to style-src. The nonce makes the browser ignore \
         'unsafe-inline', and the browser then refuses every <style> element that a \
         script creates. List \"style-src\" in \
         app.security.dangerousDisableAssetCspModification."
    );
}

#[test]
fn style_src_allows_inline_styles_with_no_nonce_or_hash() {
    let security = security_config();
    for (name, csp) in served_policies(&security) {
        let sources = directive_sources(&csp, "style-src");
        assert!(
            sources.iter().any(|source| source == "'unsafe-inline'"),
            "{name}: style-src must hold 'unsafe-inline', because xterm and the Shiki \
             style-class sheet create <style> elements at runtime: {sources:?}"
        );
        // A nonce or a hash written into the policy would make the browser
        // ignore 'unsafe-inline' exactly as the nonce from Tauri did.
        assert!(
            !sources
                .iter()
                .any(|source| source.starts_with("'nonce-") || source.starts_with("'sha")),
            "{name}: style-src must hold no nonce or hash, because either one makes \
             the browser ignore 'unsafe-inline': {sources:?}"
        );
    }
}

#[test]
fn tauri_still_adds_hashes_to_script_src() {
    let security = security_config();
    // index.html runs inline scripts, and the release script-src is only
    // 'self'. The scripts run only because Tauri adds their hashes to
    // script-src, so turning off every modification instead of style-src alone
    // would block them and leave a blank window.
    assert!(
        security
            .dangerous_disable_asset_csp_modification
            .can_modify("script-src"),
        "Tauri must keep adding the hashes of the inline scripts to script-src"
    );
}

#[test]
fn platform_configs_leave_the_security_settings_to_the_base_config() {
    // The tests above read tauri.conf.json alone. A platform file that set
    // app.security would replace that policy on its platform, and these tests
    // would not see the change.
    for (name, text) in PLATFORM_CONFIGS {
        let value: serde_json::Value = serde_json::from_str(text)
            .unwrap_or_else(|err| panic!("{name} must parse as JSON: {err}"));
        assert!(
            value.pointer("/app/security").is_none(),
            "{name} must not set app.security; set it in tauri.conf.json"
        );
    }
}
