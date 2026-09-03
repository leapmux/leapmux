//! macOS: turn a minimize REQUEST into a hide, before AppKit performs it.
//!
//! Every affordance that LeapMux offers reaches `-[NSWindow miniaturize:]`. The
//! yellow traffic light's action IS that selector, with the window as its
//! target, for a plain click and for an Option-click alike.
//! `performMiniaturize:` -- Cmd+M, the Window menu's Minimize item and a
//! title-bar double click -- calls it in turn. And tauri's own
//! `Window::minimize` calls it directly. So one override covers them all. An
//! override on a single affordance would leave the others disobeying the
//! preference.
//!
//! `-[NSApplication miniaturizeAll:]` is the measured exception. It
//! miniaturizes the window without the selector, so the override does not see
//! it. LeapMux builds no menu item that sends it, and the yellow button does
//! not send it either. So no affordance of this application reaches that path.
//!
//! An override, and not an `NSWindowDidMiniaturizeNotification` observer,
//! because nothing can undo a miniaturize once AppKit performs it. There is no
//! `windowShouldMiniaturize:`, and the notification arrives when the window is
//! already miniaturized. Neither way out of that state works:
//!
//!   * `deminiaturize:` puts the window back ON SCREEN, every time. It is
//!     asynchronous, so it does nothing in the turn that calls it. It finishes
//!     after the `orderOut:` on the next line, and restores the window that the
//!     shell just hid. An `orderOut:` first does not help either: it re-shows a
//!     window that is already off the screen list.
//!   * `orderOut:` alone leaves the window miniaturized, and the Dock tile
//!     follows `isMiniaturized` rather than the screen list. The window
//!     disappears and its tile stays in the Dock, which is the state that the
//!     preference exists to prevent.
//!
//! So the window must never enter the miniaturized state at all. The override
//! hides the window and skips `super`, which leaves it off screen and NOT
//! miniaturized: no Dock tile, and no minimize animation to undo.
//!
//! `install` builds the subclass at RUNTIME and sets it on the one main
//! window, so tao's own window class stays the superclass and keeps its
//! overrides. A swizzle of `-[NSWindow miniaturize:]` on `NSWindow` itself
//! would divert every window in the process, including the panels that AppKit
//! opens, and would then need a check on the receiver to exclude them again.

use std::ffi::CStr;
use std::panic::{catch_unwind, AssertUnwindSafe};
use std::ptr::NonNull;
use std::sync::{Arc, OnceLock};

use objc2::runtime::{AnyClass, AnyObject, ClassBuilder, Sel};
use objc2::{msg_send, sel};
use tauri::WebviewWindow;

use super::TrayState;

/// The name of the runtime subclass. An Objective-C class name is a
/// process-wide identifier that every loaded framework shares, so it carries
/// the product prefix.
const SUBCLASS_NAME: &CStr = c"LeapMuxMainWindow";

/// The prefix that Key-Value Observing (KVO) gives to the class it generates.
/// See the diagnostic in `install`.
const KVO_CLASS_PREFIX: &[u8] = b"NSKVONotifying_";

/// What the override needs to answer a minimize.
///
/// A process global, because what it describes is one. `build_subclass`
/// answers `None` for a second install, so at most one window in the process
/// ever carries the override, and the fields below always describe THAT
/// window.
struct MinimizeHook {
    /// The class that the window carried before `install` replaced it, which
    /// is the target of `super`.
    ///
    /// Recorded here, never read off the receiver. A KVO observer on the window
    /// makes the receiver's class a further subclass of this module's own, and
    /// `this.class().superclass()` would then answer that subclass and send
    /// `miniaturize:` straight back into this module.
    superclass: &'static AnyClass,
    /// Answers one minimize on the window that `install` hooked. Reports
    /// whether it hid that window.
    ///
    /// A closure over the `WebviewWindow` and the `TrayState` that `install`
    /// received, so the override acts on the window it was given and looks
    /// none up by label. It is also the seam that the tests drive: a
    /// `WebviewWindow` needs a running application, and a stub answer does not.
    answer: Box<dyn Fn() -> bool + Send + Sync>,
}

static HOOK: OnceLock<MinimizeHook> = OnceLock::new();

/// Install the minimize override on the main window.
///
/// Each branch below writes a message before it returns. Without the override
/// a minimize keeps its ordinary behaviour. That looks like "LeapMux ignores my
/// setting", and a silent return leaves the user nothing to report it with.
///
/// Standard error, and not the `BehaviorRefusal` channel that a failed tray
/// icon and a refused login item use. That channel exists for a failure the
/// MACHINE causes, which varies per user and needs the message on the
/// Preferences row. Every branch here is impossible by construction instead:
/// `tauri.conf.json` declares one window, `tray::install` calls this once for
/// the process, and no test spends the class name. And the fallback strands
/// nobody, because the ordinary minimize leaves a window in the Dock.
pub(crate) fn install(window: &WebviewWindow, state: Arc<TrayState>) {
    let Ok(ns_window) = window.ns_window() else {
        crate::shell_log!("the main window has no NSWindow; a minimize cannot hide it");
        return;
    };
    let Some(object) = NonNull::new(ns_window.cast::<AnyObject>()) else {
        crate::shell_log!("the main window's NSWindow is null; a minimize cannot hide it");
        return;
    };
    // SAFETY: `ns_window` is the live NSWindow that tauri owns and keeps for
    // the length of the process, and `install` runs on the main thread, where
    // every call below belongs.
    let object = unsafe { object.as_ref() };
    let superclass = object.class();

    // `AnyObject::class` is `object_getClass`, so it answers the LIVE class. A
    // KVO observer that AppKit added before this point makes that class a
    // generated subclass. The runtime then restores the original class when the
    // last observer goes away, and takes this override with it.
    //
    // This reports the condition and installs anyway. No first-party code
    // observes a key path on the NSWindow, the override works until that
    // restore, and a message is the only way anybody finds the cause afterwards.
    if superclass.name().to_bytes().starts_with(KVO_CLASS_PREFIX) {
        crate::shell_log!(
            "the main window is under Key-Value Observing as {}; \
             a minimize stops hiding it if that observer goes away",
            superclass.name().to_string_lossy()
        );
    }

    let Some(subclass) = build_subclass(SUBCLASS_NAME, superclass) else {
        // The name is taken, so either `install` ran twice or another bundle
        // registered it. Either way this window keeps the ordinary minimize.
        crate::shell_log!(
            "the {} class already exists; a minimize cannot hide the window",
            SUBCLASS_NAME.to_string_lossy()
        );
        return;
    };
    // `install` publishes the hook BEFORE it changes the class, so a window
    // that carries the subclass never finds the hook missing.
    let window = window.clone();
    if HOOK
        .set(MinimizeHook {
            superclass,
            answer: Box::new(move || super::handle_minimize(&state, &window)),
        })
        .is_err()
    {
        // Impossible: `build_subclass` answers `None` for a second install, so
        // a second call returns above. The arm stays, because `OnceLock::set`
        // reports a `Result` that nothing else can rule out. It returns before
        // the class changes, so the window keeps the ordinary minimize.
        crate::shell_log!("a minimize override is already published; this one is dropped");
        return;
    }
    // SAFETY: `build_subclass` built `subclass` with this window's own class as
    // its superclass and added no instance variable, so the two classes have
    // the same instance size and the object keeps its layout.
    let replaced = unsafe { AnyObject::set_class(object, subclass) };
    if !std::ptr::eq(replaced, superclass) {
        // objc2 asks the caller to check what `set_class` replaced. A different
        // class here means that something swizzled the window between the read
        // above and this line. `super` then points at the class that the other
        // party expected to keep.
        crate::shell_log!(
            "the main window changed class to {} during install; \
             a minimize may not hide it",
            replaced.name().to_string_lossy()
        );
    }
}

/// Build the subclass that overrides `miniaturize:`.
///
/// `superclass` is the class that the window carries right now, which is tao's,
/// so the subclass inherits every override that tao installed.
///
/// `name` is `SUBCLASS_NAME` in the shell. Each test passes its own name,
/// because `objc_registerClassPair` cannot be undone and a name registers once
/// for the whole process.
///
/// Answers `None` when the runtime refuses the name, which includes a second
/// install. That refusal is what keeps one window, and one only, carrying the
/// override.
fn build_subclass(name: &CStr, superclass: &AnyClass) -> Option<&'static AnyClass> {
    let mut builder = ClassBuilder::new(name, superclass)?;
    // SAFETY: the function has the shape that `-[NSWindow miniaturize:]` is
    // called with -- an object receiver, the selector, one object argument, and
    // no return value. `the_override_matches_the_signature_of_nswindow_miniaturize`
    // compares the registered encoding against AppKit's own, because objc2
    // cannot: wry turns on `objc2/disable-encoding-assertions`, and cargo
    // unifies that feature onto the one objc2 build that this crate links, so
    // `add_method` checks the argument COUNT and nothing else.
    //
    // The argument types are placeholders on purpose. The receiver spelled
    // `&AnyObject` makes the cast a `for<'a>` function pointer, and
    // `MethodImplementation` is implemented for one specific lifetime, so the
    // named form fails to compile with "not general enough".
    unsafe {
        builder.add_method(
            sel!(miniaturize:),
            miniaturize as extern "C-unwind" fn(_, _, _),
        );
    }
    Some(builder.register())
}

/// `-[NSWindow miniaturize:]` on the main window.
///
/// `super` runs for every answer except "hide". The preference keeps the window
/// in the Dock, the tray failed to build, or the answer itself failed. Each one
/// means the ordinary minimize, which is the safe direction, because the user
/// keeps a window they can reach.
///
/// The answer runs inside `catch_unwind`, because a panic that unwinds into the
/// AppKit frame that sent `miniaturize:` skips every Objective-C cleanup on the
/// way out, and reports a stack with no LeapMux frame in it. tao installs the
/// same barrier at each of its own callbacks (`stop_app_on_panic`). A panic
/// reads as "not hidden", so the ordinary minimize proceeds.
extern "C-unwind" fn miniaturize(this: &AnyObject, _cmd: Sel, sender: *mut AnyObject) {
    let hook = HOOK.get();
    let hidden = hook.is_some_and(|hook| {
        catch_unwind(AssertUnwindSafe(|| (hook.answer)())).unwrap_or_else(|_| {
            crate::shell_log!("the minimize policy panicked; the window minimizes as usual");
            false
        })
    });
    if hidden {
        return;
    }
    // The hook holds the class that `install` replaced. `install` publishes the
    // hook before it changes the class, so no window can carry this override
    // without one -- but a missing hook must still reach `super`, and the
    // registered subclass knows the same class by name.
    let Some(superclass) = hook
        .map(|hook| hook.superclass)
        .or_else(|| AnyClass::get(SUBCLASS_NAME).and_then(AnyClass::superclass))
    else {
        return;
    };
    // SAFETY: `superclass` is the class that the window carried before
    // `install` replaced it, and `miniaturize:` takes one object and returns
    // nothing.
    let _: () = unsafe { msg_send![super(this, superclass), miniaturize: sender] };
}

#[cfg(test)]
mod tests {
    use std::ffi::CStr;
    use std::ptr;
    use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};

    use objc2::rc::Retained;
    use objc2::runtime::{AnyClass, AnyObject, ClassBuilder, NSObject, Sel};
    use objc2::{msg_send, sel, ClassType};

    use super::{build_subclass, MinimizeHook, HOOK, SUBCLASS_NAME};

    /// How many times a stand-in superclass answered `miniaturize:`.
    static SUPER_CALLS: AtomicUsize = AtomicUsize::new(0);
    /// What the stub hook answers for the next `miniaturize:`.
    static HOOK_HIDES: AtomicBool = AtomicBool::new(false);
    /// Whether the stub hook panics instead of answering.
    static HOOK_PANICS: AtomicBool = AtomicBool::new(false);

    extern "C-unwind" fn count_miniaturize(_this: &AnyObject, _cmd: Sel, _sender: *mut AnyObject) {
        SUPER_CALLS.fetch_add(1, Ordering::SeqCst);
    }

    /// A stand-in for tao's window class: it answers `miniaturize:` and counts.
    ///
    /// Rooted at `NSObject` rather than `NSWindow`, so a test can create an
    /// instance of a subclass without a window server.
    fn stub_window_class(name: &CStr) -> &'static AnyClass {
        let mut builder =
            ClassBuilder::new(name, NSObject::class()).expect("the stand-in class must register");
        // SAFETY: the same shape as the override under test.
        unsafe {
            builder.add_method(
                sel!(miniaturize:),
                count_miniaturize as extern "C-unwind" fn(_, _, _),
            );
        }
        builder.register()
    }

    /// Whether `class` defines `miniaturize:` ITSELF.
    ///
    /// `instance_method` is `class_getInstanceMethod`, which searches the
    /// superclasses too. Every superclass in this module answers
    /// `miniaturize:`, so that call reports `Some` for a subclass that adds
    /// nothing at all. `instance_methods` is `class_copyMethodList`, which
    /// lists the class's own methods alone.
    fn defines_miniaturize(class: &AnyClass) -> bool {
        class
            .instance_methods()
            .iter()
            .any(|method| method.name() == sel!(miniaturize:))
    }

    // The class that `install` builds: it registers, it carries the override,
    // and the class that the window arrived with stays its superclass, so tao's
    // own overrides survive.
    #[test]
    fn the_subclass_keeps_the_window_class_as_its_superclass() {
        let superclass = stub_window_class(c"LeapMuxSubclassShapeBase");
        let subclass = build_subclass(c"LeapMuxSubclassShape", superclass)
            .expect("the runtime must accept the subclass");

        assert_eq!(subclass.name().to_bytes(), b"LeapMuxSubclassShape");
        assert!(
            defines_miniaturize(subclass),
            "the override must be on the subclass and not inherited"
        );
        assert_eq!(
            subclass.superclass().map(AnyClass::name),
            Some(superclass.name()),
            "the window's own class must stay the superclass"
        );
    }

    // A second build under the same name must REFUSE. `install` sets the class
    // only when the build succeeds, so a name that silently produced a second
    // class would let a repeat install make the subclass its own superclass --
    // and would let a second window carry the override, which every field of
    // `MinimizeHook` assumes cannot happen.
    #[test]
    fn a_second_build_under_the_same_name_is_refused() {
        let superclass = stub_window_class(c"LeapMuxSecondBuildBase");
        assert!(build_subclass(c"LeapMuxSecondBuild", superclass).is_some());
        assert!(build_subclass(c"LeapMuxSecondBuild", superclass).is_none());
    }

    // The Objective-C signature of the override, against AppKit's own.
    //
    // objc2 verifies this itself, but not here: wry turns on
    // `objc2/disable-encoding-assertions`, and cargo unifies that feature onto
    // the one objc2 build that this crate links, so `add_method` checks the
    // argument COUNT alone. A changed argument type or return type would
    // register a method whose encoding contradicts the way AppKit calls it, and
    // the first real minimize would read its arguments from the wrong place.
    //
    // `NSWindow` resolves in any process that links AppKit, and the test
    // creates no window, so this needs no window server.
    #[test]
    fn the_override_matches_the_signature_of_nswindow_miniaturize() {
        let nswindow = AnyClass::get(c"NSWindow").expect("AppKit must register NSWindow");
        let appkit = nswindow
            .instance_method(sel!(miniaturize:))
            .expect("NSWindow must answer miniaturize:");
        let subclass = build_subclass(c"LeapMuxSignatureShape", nswindow)
            .expect("the runtime must accept the subclass");
        assert!(
            defines_miniaturize(subclass),
            "without its own method the comparison below reads NSWindow twice"
        );
        let ours = subclass
            .instance_method(sel!(miniaturize:))
            .expect("the override must be on the subclass");

        assert_eq!(
            ours.return_type().to_bytes(),
            appkit.return_type().to_bytes(),
            "the override must return what AppKit returns"
        );
        assert_eq!(
            ours.arguments_count(),
            appkit.arguments_count(),
            "the override must take the arguments AppKit passes"
        );
        for index in 0..appkit.arguments_count() {
            assert_eq!(
                ours.argument_type(index).map(|ty| ty.to_bytes().to_vec()),
                appkit.argument_type(index).map(|ty| ty.to_bytes().to_vec()),
                "argument {index} must carry AppKit's encoding"
            );
        }
    }

    // The whole dispatch, end to end: the Objective-C runtime calls the
    // override, the override reads the live answer, and `super` runs for every
    // answer except "hide". It also pins the barrier that keeps a panic out of
    // the AppKit frame that sent `miniaturize:`.
    //
    // ONE test, because `HOOK` publishes once for the process. It drives every
    // answer through the two statics that the stub `answer` reads on each call,
    // the way the real one reads `TrayState`.
    #[test]
    fn the_override_calls_super_unless_the_answer_hides_the_window() {
        let superclass = stub_window_class(c"LeapMuxDispatchBase");
        let subclass = build_subclass(c"LeapMuxDispatch", superclass)
            .expect("the runtime must accept the subclass");
        HOOK.set(MinimizeHook {
            superclass,
            answer: Box::new(|| {
                assert!(
                    !HOOK_PANICS.load(Ordering::SeqCst),
                    "the minimize policy failed"
                );
                HOOK_HIDES.load(Ordering::SeqCst)
            }),
        })
        .unwrap_or_else(|_| panic!("only this test publishes the hook"));
        // SAFETY: `subclass` descends from `NSObject`, which answers `new`.
        let window: Retained<AnyObject> = unsafe { msg_send![subclass, new] };
        let minimize = || {
            // SAFETY: the receiver carries the override, and `miniaturize:`
            // takes one object and returns nothing.
            let _: () = unsafe { msg_send![&*window, miniaturize: ptr::null_mut::<AnyObject>()] };
            SUPER_CALLS.load(Ordering::SeqCst)
        };

        // "Keep in the Dock": the ordinary minimize must proceed.
        HOOK_HIDES.store(false, Ordering::SeqCst);
        assert_eq!(minimize(), 1, "super must run when the answer does not hide");

        // "Hide to the menu bar": the window must never reach `super`, or it
        // enters the miniaturized state that this module exists to prevent.
        HOOK_HIDES.store(true, Ordering::SeqCst);
        assert_eq!(minimize(), 1, "super must not run when the answer hides");

        // And back, because the override reads the answer on every click. A
        // preference that the user changes mid-session must reach the next
        // minimize.
        HOOK_HIDES.store(false, Ordering::SeqCst);
        assert_eq!(minimize(), 2, "the answer is read on every call");

        // A panic must stop at the override. Without `catch_unwind` it unwinds
        // through the Objective-C frames that dispatched the selector, and this
        // assertion never runs.
        //
        // The panic report is left alone on purpose. `std::panic::set_hook` is
        // process-global, and `cargo test` runs this file beside ninety other
        // tests on a thread pool, so a silencing hook would swallow a real
        // failure in one of them. libtest captures the expected report instead,
        // and prints it only when THIS test fails.
        HOOK_PANICS.store(true, Ordering::SeqCst);
        let calls = minimize();
        HOOK_PANICS.store(false, Ordering::SeqCst);
        assert_eq!(calls, 3, "a panic must leave the ordinary minimize alone");
    }

    // No test may register the name that `install` passes: `install` reaches
    // `set_class` only when the build succeeds, so a test that spent the name
    // would disable the override for the whole process.
    //
    // The guard is one-directional. `objc_registerClassPair` cannot be undone
    // and libtest fixes no order, so an offending test is caught only on the
    // runs where this one starts first. Each test above therefore also names
    // its own class, which is what makes the collision impossible rather than
    // merely detected.
    #[test]
    fn no_test_spends_the_class_name_that_the_shell_installs_under() {
        assert!(AnyClass::get(SUBCLASS_NAME).is_none());
    }
}
