//! Tests that pin that the shell -- not Tauri -- creates the `main` window.
//!
//! `tauri.conf.json` sets `"create": false` on it, so `setup` builds it with
//! `WebviewWindowBuilder::from_config`, which is where `on_new_window` is
//! refused. A Tauri-created window would skip that refusal, and nothing at
//! compile time would notice.

use tauri::utils::{acl::capability::Capability, config::Config};

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

#[test]
fn the_main_window_sets_create_false() {
    let config: Config =
        serde_json::from_str(BASE_CONFIG).expect("tauri.conf.json must parse as a Tauri config");
    let window = config
        .app
        .windows
        .iter()
        .find(|window| window.label == crate::MAIN_WINDOW_LABEL)
        .unwrap_or_else(|| {
            panic!(
                "tauri.conf.json must declare the {} window",
                crate::MAIN_WINDOW_LABEL
            )
        });
    assert!(
        !window.create,
        "the main window must set \"create\": false: setup builds it through \
         WebviewWindowBuilder::from_config, where new windows are refused. A \
         window Tauri creates from the config skips that refusal."
    );
    // Tauri merges a platform file over the base config by JSON Merge Patch,
    // which replaces an array wholesale: an `app.windows` list here would
    // drop the base window entry and its `create` field with it.
    for (name, text) in PLATFORM_CONFIGS {
        let value: serde_json::Value = serde_json::from_str(text)
            .unwrap_or_else(|err| panic!("{name} must parse as JSON: {err}"));
        assert!(
            value.pointer("/app/windows").is_none(),
            "{name} must not set app.windows; a replaced list must repeat \
             \"create\": false to keep the shell building the window"
        );
    }
}

#[test]
fn the_main_webview_cannot_create_native_webviews() {
    let capability: Capability = serde_json::from_str(include_str!("../capabilities/default.json"))
        .expect("the default capability must parse as JSON");
    assert_eq!(capability.windows, [crate::MAIN_WINDOW_LABEL]);
    assert!(capability.webviews.is_empty());
    assert!(capability.remote.is_none());
    for permission in [
        "core:webview:allow-create-webview",
        "core:webview:allow-create-webview-window",
        "core:window:allow-create",
    ] {
        assert!(
            !capability
                .permissions
                .iter()
                .any(|entry| entry.identifier().as_ref() == permission),
            "the default capability must not grant {permission}"
        );
    }
}

#[test]
fn the_notice_window_loads_only_the_bundled_notice() {
    match crate::notice_window_url() {
        tauri::WebviewUrl::App(path) => assert_eq!(path, std::path::Path::new("NOTICE.html")),
        _ => panic!("the licenses window must start on a bundled asset"),
    }
}
