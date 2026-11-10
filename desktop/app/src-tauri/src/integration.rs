//! "Copy a share link" and "Open on the NAS" in the menu of files of the
//! system's file manager. They start this app with `--share-link PATH` or
//! `--open-on-nas PATH`; the running instance gets them (single instance)
//! and the window does the work for files inside synchronized folders.
//!
//! - Windows: verbs of the user's file classes (HKCU\Software\Classes\*\shell
//!   and Directory\shell), shown under "Show more options" on Windows 11 (a
//!   menu of the new style needs a packaged shell extension: not done).
//! - Linux: a Nautilus (GNOME Files) script folder and a Dolphin (KDE)
//!   service menu.
//! - macOS: not done (it needs a Finder Sync extension, a separate signed
//!   target).

use serde::{Deserialize, Serialize};

/// Texts of the menu items, in the user's language (from the window).
#[derive(Deserialize, Clone)]
pub struct Labels {
    pub share: String,
    pub open: String,
}

#[derive(Serialize)]
pub struct Status {
    pub supported: bool,
    pub enabled: bool,
    pub how: &'static str,
}

/// File name safe for a menu label (Nautilus shows the script's name).
#[allow(dead_code)]
pub fn safe_name(label: &str) -> String {
    label
        .chars()
        .map(|c| {
            if c == '/' || c == '\\' || c == '\0' || c.is_control() {
                ' '
            } else {
                c
            }
        })
        .collect::<String>()
        .trim()
        .to_string()
}

/// Quotes a path for a POSIX shell script.
#[allow(dead_code)]
pub fn sh_quote(s: &str) -> String {
    format!("'{}'", s.replace('\'', "'\\''"))
}

#[cfg(target_os = "linux")]
mod imp {
    use super::*;
    use std::fs;
    use std::path::PathBuf;

    fn home() -> PathBuf {
        PathBuf::from(std::env::var_os("HOME").unwrap_or_default())
    }
    fn data_home() -> PathBuf {
        std::env::var_os("XDG_DATA_HOME")
            .filter(|v| !v.is_empty())
            .map(PathBuf::from)
            .unwrap_or_else(|| home().join(".local/share"))
    }
    fn nautilus_dir() -> PathBuf {
        data_home().join("nautilus/scripts/StrataOS")
    }
    fn dolphin_file() -> PathBuf {
        data_home().join("kio/servicemenus/strataos.desktop")
    }

    fn exe() -> String {
        // An AppImage runs from a temporary mount: the menu must start the AppImage file itself.
        std::env::var("APPIMAGE")
            .ok()
            .filter(|v| !v.is_empty())
            .unwrap_or_else(|| {
                std::env::current_exe()
                    .map(|p| p.to_string_lossy().into_owned())
                    .unwrap_or_else(|_| "strataos-desktop".into())
            })
    }

    pub fn status() -> Status {
        Status {
            supported: true,
            enabled: nautilus_dir().exists() || dolphin_file().exists(),
            how: "nautilus+dolphin",
        }
    }

    pub fn set(on: bool, labels: &Labels) -> Result<(), String> {
        let _ = fs::remove_dir_all(nautilus_dir());
        let _ = fs::remove_file(dolphin_file());
        if !on {
            return Ok(());
        }
        let exe = exe();
        let dir = nautilus_dir();
        fs::create_dir_all(&dir).map_err(|e| e.to_string())?;
        for (label, flag) in [
            (&labels.share, "--share-link"),
            (&labels.open, "--open-on-nas"),
        ] {
            let script = format!(
                "#!/bin/sh\n# Written by the StrataOS app (Settings, file manager menu).\nfor f in \"$@\"; do\n  case \"$f\" in /*) ;; *) f=\"$PWD/$f\" ;; esac\n  exec {} {} \"$f\"\ndone\n",
                sh_quote(&exe),
                flag
            );
            let p = dir.join(safe_name(label));
            fs::write(&p, script).map_err(|e| e.to_string())?;
            use std::os::unix::fs::PermissionsExt;
            fs::set_permissions(&p, fs::Permissions::from_mode(0o755))
                .map_err(|e| e.to_string())?;
        }
        let d = dolphin_file();
        if let Some(parent) = d.parent() {
            fs::create_dir_all(parent).map_err(|e| e.to_string())?;
        }
        let q = exe.replace('"', "\\\"");
        let menu = format!(
            "[Desktop Entry]\nType=Service\nMimeType=application/octet-stream;inode/directory;\nActions=share;open\nX-KDE-Submenu=StrataOS\n\n[Desktop Action share]\nName={}\nIcon=strataos-desktop\nExec=\"{q}\" --share-link %f\n\n[Desktop Action open]\nName={}\nIcon=strataos-desktop\nExec=\"{q}\" --open-on-nas %f\n",
            labels.share.replace('\n', " "),
            labels.open.replace('\n', " ")
        );
        fs::write(&d, menu).map_err(|e| e.to_string())?;
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(&d, fs::Permissions::from_mode(0o755)).map_err(|e| e.to_string())?;
        Ok(())
    }
}

#[cfg(windows)]
mod imp {
    use super::*;
    use winreg::enums::HKEY_CURRENT_USER;
    use winreg::RegKey;

    const KEYS: [&str; 2] = [
        r"Software\Classes\*\shell",
        r"Software\Classes\Directory\shell",
    ];
    const VERBS: [&str; 2] = ["StrataOS.ShareLink", "StrataOS.OpenOnNas"];

    pub fn status() -> Status {
        let hkcu = RegKey::predef(HKEY_CURRENT_USER);
        let on = hkcu
            .open_subkey(format!(r"{}\{}", KEYS[0], VERBS[0]))
            .is_ok();
        Status {
            supported: true,
            enabled: on,
            how: "explorer",
        }
    }

    pub fn set(on: bool, labels: &Labels) -> Result<(), String> {
        let hkcu = RegKey::predef(HKEY_CURRENT_USER);
        for k in KEYS {
            for v in VERBS {
                let _ = hkcu.delete_subkey_all(format!(r"{k}\{v}"));
            }
        }
        if !on {
            return Ok(());
        }
        let exe = std::env::current_exe()
            .map_err(|e| e.to_string())?
            .to_string_lossy()
            .into_owned();
        for k in KEYS {
            for (v, label, flag) in [
                (VERBS[0], &labels.share, "--share-link"),
                (VERBS[1], &labels.open, "--open-on-nas"),
            ] {
                let (key, _) = hkcu
                    .create_subkey(format!(r"{k}\{v}"))
                    .map_err(|e| e.to_string())?;
                key.set_value("", label).map_err(|e| e.to_string())?;
                key.set_value("Icon", &exe).map_err(|e| e.to_string())?;
                let (cmd, _) = key.create_subkey("command").map_err(|e| e.to_string())?;
                cmd.set_value("", &format!("\"{exe}\" {flag} \"%1\""))
                    .map_err(|e| e.to_string())?;
            }
        }
        Ok(())
    }
}

#[cfg(not(any(windows, target_os = "linux")))]
mod imp {
    use super::*;
    pub fn status() -> Status {
        Status {
            supported: false,
            enabled: false,
            how: "",
        }
    }
    pub fn set(_on: bool, _labels: &Labels) -> Result<(), String> {
        Err("not available on this system".into())
    }
}

pub use imp::{set, status};

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn names_and_quotes() {
        assert_eq!(safe_name(" Copy a/share link\n"), "Copy a share link");
        assert_eq!(sh_quote("/opt/it's here/app"), "'/opt/it'\\''s here/app'");
    }
}
