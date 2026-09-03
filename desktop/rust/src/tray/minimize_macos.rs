//! macOS: turn a minimize REQUEST into a hide, before AppKit performs it.
//!
//! Every way a window can be miniaturized reaches `-[NSWindow miniaturize:]`.
//! The yellow traffic light's action IS that selector, with the window as its
//! target. `performMiniaturize:` -- Cmd+M, the Window menu's Minimize item and
//! a title-bar double click -- calls it in turn. And tauri's own
//! `Window::minimize` calls it directly. So one override covers every
//! affordance, where intercepting a single one would leave the others
//! disobeying the preference.
//!
//! An override, and not an `NSWindowDidMiniaturizeNotification` observer,
//! because a miniaturize CANNOT be undone once AppKit performs it. There is no
//! `windowShouldMiniaturize:`, and the notification arrives when the window is
//! already miniaturized. Neither way out of that state works:
//!
//!   * `deminiaturize:` puts the window back ON SCREEN, every time. It is
//!     asynchronous, so it does nothing in the turn that calls it and finishes
//!     after the `orderOut:` on the next line -- which restores the window the
//!     shell just hid. Ordering the window out first does not help either: it
//!     re-shows a window that is already off the screen list.
//!   * `orderOut:` alone leaves the window miniaturized, and the Dock tile
//!     follows `isMiniaturized` rather than the screen list. The window
//!     disappears and its tile stays in the Dock, which is the state the
//!     preference exists to prevent.
//!
//! So the window must never enter the miniaturized state at all. The override
//! hides it instead of calling `super`, which leaves it off screen and NOT
//! miniaturized: no Dock tile, and no minimize animation to undo.
//!
//! The subclass is built at RUNTIME and set on the one main window, so tao's
//! own window class stays the superclass and keeps its overrides. Swizzling
//! `-[NSWindow miniaturize:]` on that class instead would divert every other
//! window in the process too, and would then need a check on the receiver to
//! exclude them again.

use std::ffi::CStr;
use std::ptr::NonNull;
use std::sync::{Arc, OnceLock};

use objc2::runtime::{AnyClass, AnyObject, ClassBuilder, Sel};
use objc2::{msg_send, sel};
use tauri::{AppHandle, Manager, WebviewWindow};

use super::TrayState;

/// The name of the runtime subclass. An Objective-C class name is a
/// process-wide identifier that every loaded framework shares, so it carries
/// the product prefix.
const SUBCLASS_NAME: &CStr = c"LeapMuxMainWindow";

/// What the override needs to answer a minimize.
///
/// A process global, because what it describes is one. The shell has one main
/// window, and `objc_allocateClassPair` refuses a name that is already
/// registered, so the subclass exists at most once.
struct MinimizeHook {
    state: Arc<TrayState>,
    app: AppHandle,
}

static HOOK: OnceLock<MinimizeHook> = OnceLock::new();

/// Install the minimize override on the main window.
///
/// Each branch below logs before it returns. Without the override a minimize
/// keeps its ordinary behaviour, which looks like "LeapMux ignores my setting",
/// and a silent return leaves the user nothing to report it with.
pub(crate) fn install(window: &WebviewWindow, state: Arc<TrayState>) {
    let Ok(ns_window) = window.ns_window() else {
        eprintln!("leapmux-desktop: the main window has no NSWindow; a minimize cannot hide it");
        return;
    };
    let Some(object) = NonNull::new(ns_window.cast::<AnyObject>()) else {
        eprintln!("leapmux-desktop: the main window's NSWindow is null; a minimize cannot hide it");
        return;
    };
    // SAFETY: `ns_window` is the live NSWindow that tauri owns and keeps for
    // the length of the process, and `install` runs on the main thread, where
    // every call below belongs.
    let object = unsafe { object.as_ref() };

    // The hook is published BEFORE the class changes. The override reads it, so
    // a window that carries the subclass must never find it missing.
    if HOOK
        .set(MinimizeHook {
            state,
            app: window.app_handle().clone(),
        })
        .is_err()
    {
        // Already installed. Setting the class a second time would make the
        // subclass its own superclass.
        eprintln!("leapmux-desktop: the minimize override is already installed");
        return;
    }
    let Some(subclass) = build_subclass(object.class()) else {
        eprintln!(
            "leapmux-desktop: the runtime refused the {} class; a minimize cannot hide the window",
            SUBCLASS_NAME.to_string_lossy()
        );
        return;
    };
    // SAFETY: `subclass` was built with this window's own class as its
    // superclass, so it adds one method and changes no instance layout.
    unsafe { AnyObject::set_class(object, subclass) };
}

/// Build the subclass that overrides `miniaturize:`.
///
/// `superclass` is the class the window carries right now, which is tao's, so
/// the subclass inherits every override tao installed.
fn build_subclass(superclass: &AnyClass) -> Option<&'static AnyClass> {
    let mut builder = ClassBuilder::new(SUBCLASS_NAME, superclass)?;
    // SAFETY: the function has the shape `-[NSWindow miniaturize:]` is called
    // with -- an object receiver, the selector, one object argument, and no
    // return value.
    //
    // The argument types are placeholders on purpose. Spelling the receiver
    // `&AnyObject` makes the cast a `for<'a>` function pointer, and
    // `MethodImplementation` is implemented for one specific lifetime, so the
    // named form fails to compile with "not general enough".
    unsafe {
        builder.add_method(sel!(miniaturize:), miniaturize as extern "C-unwind" fn(_, _, _));
    }
    Some(builder.register())
}

/// `-[NSWindow miniaturize:]` on the main window.
///
/// `super` runs for every answer except "hide": the preference keeps the window
/// in the Dock, the tray failed to build, or -- impossible, because `install`
/// publishes the hook before it sets the class -- the hook is absent. Each of
/// those means the ordinary minimize, which is the safe direction, because the
/// user keeps a window they can reach.
extern "C-unwind" fn miniaturize(this: &AnyObject, _cmd: Sel, sender: *mut AnyObject) {
    if hide_instead_of_minimizing() {
        return;
    }
    let Some(superclass) = subclass_superclass() else {
        return;
    };
    // SAFETY: `superclass` is the class the window carried before `install`
    // replaced it, and `miniaturize:` takes one object and returns nothing.
    let _: () = unsafe { msg_send![super(this, superclass), miniaturize: sender] };
}

/// Hide the main window if the live policy turns a minimize into a hide.
///
/// Returns whether it hid, which is what decides `super`.
fn hide_instead_of_minimizing() -> bool {
    let Some(hook) = HOOK.get() else {
        return false;
    };
    let Some(window) = hook.app.get_webview_window("main") else {
        return false;
    };
    super::handle_minimize(&hook.state, &window)
}

/// The class the main window carried before `install` replaced it.
///
/// Looked up through the SUBCLASS by name, never through the receiver's own
/// class. A KVO observer on the window makes that receiver's class a further
/// subclass of this one, and `this.class().superclass()` would then answer the
/// subclass itself and send `miniaturize:` straight back into this module.
fn subclass_superclass() -> Option<&'static AnyClass> {
    AnyClass::get(SUBCLASS_NAME)?.superclass()
}

#[cfg(test)]
mod tests {
    use objc2::runtime::{AnyClass, ClassBuilder, NSObject};
    use objc2::sel;

    use super::{build_subclass, subclass_superclass, SUBCLASS_NAME};

    // The class surgery `install` performs, on a plain `NSObject` rather than
    // the main window: the Objective-C runtime is available in any process, and
    // a window server is not. It pins what the compiler cannot.
    //
    // ONE test for the whole contract, because a class name registers once per
    // process. Split across several, the assertions would race for the single
    // registration that any one of them can make.
    #[test]
    fn the_subclass_registers_once_and_answers_super_through_its_own_name() {
        let object = NSObject::new();
        let superclass = object.class();

        // The build. The subclass registers, it carries the override, and the
        // class the window arrived with stays its superclass, so tao's own
        // overrides survive.
        let subclass = build_subclass(superclass).expect("the runtime must accept the subclass");
        assert_eq!(subclass.name(), SUBCLASS_NAME);
        assert!(
            subclass.instance_method(sel!(miniaturize:)).is_some(),
            "the override must be on the subclass"
        );
        assert_eq!(
            subclass.superclass().map(AnyClass::name),
            Some(superclass.name()),
            "the window's own class must stay the superclass"
        );

        // A second build must REFUSE. `install` sets the class only when the
        // build succeeds, so a name that silently produced a second class would
        // let a repeat install make the subclass its own superclass.
        assert!(build_subclass(superclass).is_none());

        // The `super` target, which the override sends `miniaturize:` to.
        assert_eq!(
            subclass_superclass().map(AnyClass::name),
            Some(superclass.name())
        );

        // The recursion guard. A KVO observer on the window replaces that
        // window's class with a further subclass of this one, so the RECEIVER's
        // superclass becomes the subclass itself -- and an override that read
        // its super target there would send `miniaturize:` back into itself
        // until the stack ran out. The stand-in below has the shape KVO's
        // generated class has, under a name only this test owns.
        let receiver_class = ClassBuilder::new(c"LeapMuxMainWindowTestObserver", subclass)
            .expect("the stand-in for a KVO subclass must register")
            .register();

        // The two lookups now part company, which is the whole reason this
        // module resolves `super` through the registered NAME.
        assert_eq!(
            receiver_class.superclass().map(AnyClass::name),
            Some(SUBCLASS_NAME),
            "reading the super target off the receiver would recurse"
        );
        assert_eq!(
            subclass_superclass().map(AnyClass::name),
            Some(superclass.name()),
            "reading it off the name reaches the class the window arrived with"
        );
        assert_ne!(
            subclass_superclass().map(AnyClass::name),
            receiver_class.superclass().map(AnyClass::name)
        );
    }
}
