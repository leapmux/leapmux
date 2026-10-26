//! Apply login-item preferences and report operating-system refusals.

pub(crate) trait LoginItemManager {
    type Error: std::fmt::Display;
    fn enable(&self) -> Result<(), Self::Error>;
    fn disable(&self) -> Result<(), Self::Error>;
    fn is_enabled(&self) -> Result<bool, Self::Error>;
}

impl LoginItemManager for tauri_plugin_autostart::AutoLaunchManager {
    type Error = tauri_plugin_autostart::Error;

    fn enable(&self) -> Result<(), Self::Error> {
        tauri_plugin_autostart::AutoLaunchManager::enable(self)
    }

    fn disable(&self) -> Result<(), Self::Error> {
        tauri_plugin_autostart::AutoLaunchManager::disable(self)
    }

    fn is_enabled(&self) -> Result<bool, Self::Error> {
        tauri_plugin_autostart::AutoLaunchManager::is_enabled(self)
    }
}

pub(crate) fn apply(manager: &impl LoginItemManager, start_on_login: bool) -> Result<(), String> {
    if start_on_login {
        // An existing registration does not prove its executable path is current.
        // Enabling rewrites the registration after the application moves.
        return manager
            .enable()
            .map_err(|err| format!("LeapMux could not add itself to your login items: {err}"));
    }
    // Check first because removing an absent registration can return an error.
    match manager.is_enabled() {
        Ok(true) => manager
            .disable()
            .map_err(|err| format!("LeapMux could not remove itself from your login items: {err}")),
        Ok(false) => Ok(()),
        Err(err) => Err(format!("LeapMux could not check your login items: {err}")),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::cell::RefCell;

    struct FakeManager {
        status: Result<bool, &'static str>,
        write: Result<(), &'static str>,
        operations: RefCell<Vec<&'static str>>,
    }

    impl LoginItemManager for FakeManager {
        type Error = &'static str;

        fn enable(&self) -> Result<(), Self::Error> {
            self.operations.borrow_mut().push("enable");
            self.write
        }

        fn disable(&self) -> Result<(), Self::Error> {
            self.operations.borrow_mut().push("disable");
            self.write
        }

        fn is_enabled(&self) -> Result<bool, Self::Error> {
            self.operations.borrow_mut().push("is_enabled");
            self.status
        }
    }

    #[test]
    fn enable_rewrites_the_entry_without_reading_status() {
        for status in [Ok(false), Ok(true), Err("read denied")] {
            for write in [Ok(()), Err("write denied")] {
                let manager = FakeManager {
                    status,
                    write,
                    operations: RefCell::default(),
                };
                let result = apply(&manager, true);
                assert_eq!(manager.operations.into_inner(), ["enable"]);
                match write {
                    Ok(()) => assert_eq!(result, Ok(())),
                    Err(_) => assert_eq!(
                        result,
                        Err(
                            "LeapMux could not add itself to your login items: write denied"
                                .to_string()
                        )
                    ),
                }
            }
        }
    }

    #[test]
    fn disable_reports_read_and_removal_failures() {
        let cases = [
            (Ok(false), Ok(()), Ok(()), vec!["is_enabled"]),
            (Ok(true), Ok(()), Ok(()), vec!["is_enabled", "disable"]),
            (
                Ok(true),
                Err("write denied"),
                Err(
                    "LeapMux could not remove itself from your login items: write denied"
                        .to_string(),
                ),
                vec!["is_enabled", "disable"],
            ),
            (
                Err("read denied"),
                Ok(()),
                Err("LeapMux could not check your login items: read denied".to_string()),
                vec!["is_enabled"],
            ),
        ];
        for (status, write, expected, operations) in cases {
            let manager = FakeManager {
                status,
                write,
                operations: RefCell::default(),
            };
            assert_eq!(apply(&manager, false), expected);
            assert_eq!(manager.operations.into_inner(), operations);
        }
    }
}
