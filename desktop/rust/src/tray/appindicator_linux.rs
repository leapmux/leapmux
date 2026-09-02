//! Linux: whether a status-icon library is present, checked BEFORE any tray
//! call.
//!
//! This lives beside the minimize hook rather than inside it. Both are Linux
//! concerns, but a reader who finds `minimize_linux::appindicator_available()`
//! at the top of `build_tray` is told that creating a tray icon depends on the
//! hook that catches a minimize, which it does not.

/// Whether a status-icon library is present.
///
/// The .deb only recommends `libayatana-appindicator3-1`, because Linux desktop
/// environments differ too much to force an indicator library on a user whose
/// desktop has no tray at all. That makes an absent library an ordinary case
/// rather than an edge one, and it must not be the case that reaches
/// `libappindicator-sys`: that crate resolves its library through a `Lazy` that
/// PANICS when both sonames are missing, and the panic would unwind through
/// GTK's C frames.
///
/// The answer is NOT cached. A user who reads the error message, installs the
/// library and turns the tray icon on again is exactly the case the message
/// asks for, and a cached "absent" would refuse them until they restart the
/// application.
///
/// The handle is leaked deliberately. Loading the library here is what makes it
/// already resident when the lazy resolve runs, so the two cannot disagree.
pub(crate) fn available() -> bool {
    for soname in [
        c"libayatana-appindicator3.so.1",
        c"libappindicator3.so.1",
    ] {
        // SAFETY: a fixed soname that no user input reaches, opened lazily and
        // never closed.
        let handle = unsafe { libc::dlopen(soname.as_ptr(), libc::RTLD_LAZY) };
        if !handle.is_null() {
            return true;
        }
    }
    false
}
