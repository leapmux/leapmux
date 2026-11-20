//! The navigation allow-list for every webview the shell serves.
//!
//! A plugin hook and not a per-window `on_navigation`, because one hook covers
//! every webview, including the licenses window that the shell opens for
//! the bundled notice. The shell states its own predicate: Tauri 2.12.5 keeps
//! `Webview::is_local_url` private, and the allow-list here is the app's
//! policy, not a guess at Tauri's.
//!
//! No Rust test reaches the plugin hook itself: the crate enables no `test`
//! feature of `tauri`, so a mock app cannot stand in for the runtime. The
//! predicate below carries the policy and takes the whole test surface.

use tauri::{Manager, Url};

/// Whether the shell may navigate a webview to `url`.
///
/// Accepts exactly the origins the bundled frontend is served from -- the
/// `tauri://localhost` custom protocol on macOS and Linux, the `http(s)://
/// tauri.localhost` hosts Windows WebView2 resolves it to -- plus the origin
/// of the dev server when one is passed, and `about:blank` (a `window.open`
/// stub before it is pointed at real content). Every other origin is refused:
/// the shell never shows a Hub page or any other remote content in a webview.
///
/// Origins are compared by scheme, host and port, so a look-alike host
/// (`tauri.localhost.evil`) and a dev server on another port stay refused.
pub(crate) fn is_app_url(url: &Url, dev_url: Option<&Url>) -> bool {
    if url.as_str() == "about:blank" {
        return true;
    }
    for (scheme, host) in [
        ("tauri", "localhost"),
        ("http", "tauri.localhost"),
        ("https", "tauri.localhost"),
    ] {
        // No port: `url` drops the default port of a special scheme, and any
        // other port is a different origin.
        if (url.scheme(), url.host_str(), url.port()) == (scheme, Some(host), None) {
            return true;
        }
    }
    match (url.host_str(), dev_url) {
        (Some(host), Some(dev)) => {
            url.scheme() == dev.scheme() && Some(host) == dev.host_str() && url.port() == dev.port()
        }
        _ => false,
    }
}

/// The navigation allow-list as a Tauri plugin.
///
/// `dev` is the config's `build.dev_url` in a dev build and `None` otherwise,
/// so a release build accepts no dev origin even if one is configured. The
/// hook logs every refusal: a navigation that silently goes nowhere reads as a
/// broken page.
pub(crate) fn plugin<R: tauri::Runtime>() -> tauri::plugin::TauriPlugin<R> {
    tauri::plugin::Builder::new("navigation")
        .on_navigation(|webview, url| {
            let dev_url = if tauri::is_dev() {
                webview.app_handle().config().build.dev_url.clone()
            } else {
                None
            };
            let allowed = is_app_url(url, dev_url.as_ref());
            if !allowed {
                crate::shell_log!("refusing navigation to {url}");
            }
            allowed
        })
        .build()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn parse(url: &str) -> Url {
        Url::parse(url).unwrap_or_else(|err| panic!("parse {url}: {err}"))
    }

    // The origins the bundled frontend is served from, in every platform's
    // spelling. A refusal on any of them leaves the app on a blank window.
    #[test]
    fn app_origins_are_accepted() {
        for url in [
            "tauri://localhost/x",
            "http://tauri.localhost/",
            "https://tauri.localhost/a?b",
        ] {
            assert!(
                is_app_url(&parse(url), None),
                "the app origin must be accepted: {url}"
            );
        }
    }

    // The dev server origin belongs to the app only in a dev build, which is
    // what a passed `dev_url` stands for here. Without it the same URL is an
    // ordinary localhost origin and must not slip through.
    #[test]
    fn the_dev_origin_is_accepted_only_with_the_dev_url() {
        let dev = parse("http://localhost:4328/");
        assert!(
            is_app_url(&parse("http://localhost:4328/"), Some(&dev)),
            "the dev origin must be accepted when the dev URL is passed"
        );
        assert!(
            !is_app_url(&parse("http://localhost:4328/"), None),
            "the dev origin must be refused when no dev URL is passed"
        );
    }

    // The allow-list is an exact list of origins, not a prefix or a suffix
    // rule: a look-alike host, another port, and a foreign scheme all land
    // outside it.
    #[test]
    fn other_origins_are_refused() {
        let dev = parse("http://localhost:4328/");
        for url in [
            "https://hub.example/",
            "http://localhost:4329/",
            "tauri://evil/",
            "file:///etc/passwd",
            "http://tauri.localhost.evil/",
        ] {
            assert!(
                !is_app_url(&parse(url), Some(&dev)),
                "an origin outside the allow-list must be refused: {url}"
            );
        }
    }

    // A webview and its subframes hold `about:blank` before they load real
    // content, and the hook sees those navigations too on macOS and Linux.
    #[test]
    fn about_blank_is_accepted() {
        assert!(is_app_url(&parse("about:blank"), None));
    }
}
