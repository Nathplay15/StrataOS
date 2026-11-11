//! Files on demand on Windows: registers the sync root with Explorer
//! (`StorageProviderSyncRootManager`, a WinRT API), which gives it its entry
//! in the navigation pane, its icon and the "Always keep on this device" /
//! "Free up space" entries of the Windows 11 menu. The agent (Go,
//! `internal/ondemand/cfapi_windows.go`) creates the placeholders and serves
//! them; it asks for this registration (state "register") and registers a
//! plain one itself when the shell does not answer.
//!
//! The policies must match the agent's: hydration Full, population
//! AlwaysFull (the agent creates every placeholder itself).

/// The ID the agent gives: `StrataOS!<user SID>!<account>`.
pub fn valid_id(id: &str) -> bool {
    let mut parts = id.split('!');
    let (Some(p), Some(sid), Some(acc), None) =
        (parts.next(), parts.next(), parts.next(), parts.next())
    else {
        return false;
    };
    p == "StrataOS"
        && sid.starts_with("S-1-")
        && sid[4..].chars().all(|c| c.is_ascii_digit() || c == '-')
        && !acc.is_empty()
        && acc.len() <= 24
        && acc
            .chars()
            .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit())
}

/// An absolute local folder (`C:\…`), no network path.
pub fn valid_path(p: &str) -> bool {
    let b = p.as_bytes();
    b.len() >= 3
        && b[0].is_ascii_alphabetic()
        && b[1] == b':'
        && b[2] == b'\\'
        && !p.contains("\\..\\")
        && !p.ends_with("\\..")
        && !p.chars().any(|c| c.is_control())
}

/// Registers (or updates) the sync root of an account.
pub fn register(id: &str, path: &str, name: &str) -> Result<(), String> {
    if !valid_id(id) || !valid_path(path) {
        return Err("invalid sync root".into());
    }
    imp::register(id, path, name.trim())
}

/// Removes the sync root of an account (files on demand turned off).
pub fn unregister(id: &str) -> Result<(), String> {
    if !valid_id(id) {
        return Err("invalid sync root".into());
    }
    imp::unregister(id)
}

#[cfg(windows)]
mod imp {
    use windows::core::HSTRING;
    use windows::Security::Cryptography::{BinaryStringEncoding, CryptographicBuffer};
    use windows::Storage::Provider::{
        StorageProviderHardlinkPolicy, StorageProviderHydrationPolicy,
        StorageProviderHydrationPolicyModifier, StorageProviderInSyncPolicy,
        StorageProviderPopulationPolicy, StorageProviderSyncRootInfo,
        StorageProviderSyncRootManager,
    };
    use windows::Storage::StorageFolder;

    fn e(err: windows::core::Error) -> String {
        err.message().to_string()
    }

    pub fn register(id: &str, path: &str, name: &str) -> Result<(), String> {
        let folder = StorageFolder::GetFolderFromPathAsync(&HSTRING::from(path))
            .map_err(e)?
            .get()
            .map_err(e)?;
        let info = StorageProviderSyncRootInfo::new().map_err(e)?;
        info.SetId(&HSTRING::from(id)).map_err(e)?;
        info.SetPath(&folder).map_err(e)?;
        info.SetDisplayNameResource(&HSTRING::from(if name.is_empty() {
            "StrataOS"
        } else {
            name
        }))
        .map_err(e)?;
        let exe = std::env::current_exe().map_err(|err| err.to_string())?;
        info.SetIconResource(&HSTRING::from(format!("{},0", exe.display())))
            .map_err(e)?;
        info.SetHydrationPolicy(StorageProviderHydrationPolicy::Full)
            .map_err(e)?;
        info.SetHydrationPolicyModifier(StorageProviderHydrationPolicyModifier::None)
            .map_err(e)?;
        info.SetPopulationPolicy(StorageProviderPopulationPolicy::AlwaysFull)
            .map_err(e)?;
        info.SetInSyncPolicy(StorageProviderInSyncPolicy::Default)
            .map_err(e)?;
        info.SetHardlinkPolicy(StorageProviderHardlinkPolicy::None)
            .map_err(e)?;
        info.SetShowSiblingsAsGroup(false).map_err(e)?;
        info.SetAllowPinning(true).map_err(e)?;
        info.SetVersion(&HSTRING::from(env!("CARGO_PKG_VERSION")))
            .map_err(e)?;
        let ctx = CryptographicBuffer::ConvertStringToBinary(
            &HSTRING::from(id),
            BinaryStringEncoding::Utf8,
        )
        .map_err(e)?;
        info.SetContext(&ctx).map_err(e)?;
        StorageProviderSyncRootManager::Register(&info).map_err(e)
    }

    pub fn unregister(id: &str) -> Result<(), String> {
        StorageProviderSyncRootManager::Unregister(&HSTRING::from(id)).map_err(e)
    }
}

#[cfg(not(windows))]
mod imp {
    pub fn register(_id: &str, _path: &str, _name: &str) -> Result<(), String> {
        Err("Windows only".into())
    }
    pub fn unregister(_id: &str) -> Result<(), String> {
        Err("Windows only".into())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn ids_and_paths() {
        assert!(valid_id(
            "StrataOS!S-1-5-21-1004336348-1177238915-682003330-1001!main"
        ));
        assert!(valid_id("StrataOS!S-1-5-21-1!a1b2c3d4"));
        assert!(!valid_id("OneDrive!S-1-5-21-1!main"));
        assert!(!valid_id("StrataOS!S-1-5-21-1!Main"));
        assert!(!valid_id("StrataOS!S-1-5-21-1!main!x"));
        assert!(!valid_id("StrataOS!x!main"));
        assert!(!valid_id("StrataOS!S-1-5!"));
        assert!(valid_path(r"C:\Users\me\StrataOS - NAS"));
        assert!(!valid_path(r"\\server\share"));
        assert!(!valid_path(r"C:\Users\..\Windows"));
        assert!(!valid_path("relative"));
        assert!(register("bad", r"C:\x", "n").is_err());
    }
}
