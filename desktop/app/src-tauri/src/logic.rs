//! Pure logic of the shell, kept apart for unit tests: the agent's first
//! line, the actions given on the command line (Explorer and Finder menus,
//! pairing links), the addresses a web window may open.

use serde::{Deserialize, Serialize};

/// First line printed by `strataos-sync agent`.
#[derive(Debug, Deserialize, PartialEq)]
pub struct Handshake {
    pub agent: u32,
    pub port: u16,
    #[serde(default)]
    pub version: String,
}

/// Reads the agent's first line ("{"agent":1,"port":N}").
pub fn parse_handshake(line: &str) -> Option<Handshake> {
    let h: Handshake = serde_json::from_str(line.trim()).ok()?;
    if h.agent != 1 || h.port == 0 {
        return None;
    }
    Some(h)
}

/// A random token for the agent's API (hex, 48 characters).
pub fn new_token() -> String {
    use rand::RngCore;
    let mut b = [0u8; 24];
    rand::thread_rng().fill_bytes(&mut b);
    hex::encode(b)
}

/// Something the app was asked to do from outside: a menu of the file
/// manager, a pairing link.
#[derive(Debug, Clone, Serialize, PartialEq)]
#[serde(tag = "kind", rename_all = "camelCase")]
pub enum Action {
    ShareLink { path: String },
    OpenOnNas { path: String },
    DeepLink { url: String },
}

/// Reads the actions of a command line (the program name first).
pub fn actions_from_args<I: IntoIterator<Item = String>>(args: I) -> Vec<Action> {
    let mut out = Vec::new();
    let mut it = args.into_iter().skip(1);
    while let Some(a) = it.next() {
        match a.as_str() {
            "--share-link" | "--open-on-nas" => {
                if let Some(p) = it.next() {
                    if p.is_empty() {
                        continue;
                    }
                    out.push(if a == "--share-link" {
                        Action::ShareLink { path: p }
                    } else {
                        Action::OpenOnNas { path: p }
                    });
                }
            }
            _ if a.starts_with("strataos://") => out.push(Action::DeepLink { url: a }),
            _ => {}
        }
    }
    out
}

/// Whether the app was started at login (hidden window).
pub fn is_background<I: IntoIterator<Item = String>>(args: I) -> bool {
    args.into_iter().any(|a| a == "--background")
}

/// A web window only opens the agent's own proxy (127.0.0.1): never
/// another site with the app's privileges.
pub fn web_window_allowed(u: &str) -> bool {
    match url::Url::parse(u) {
        Ok(p) => {
            p.scheme() == "http"
                && p.host_str() == Some("127.0.0.1")
                && p.port().is_some()
                && p.username().is_empty()
                && p.password().is_none()
        }
        Err(_) => false,
    }
}

/// Icon shown by the tray for a state sent by the window.
pub fn tray_icon_name(state: &str) -> &'static str {
    match state {
        "syncing" => "syncing",
        "error" => "error",
        "paused" => "paused",
        _ => "idle",
    }
}

/// Converts the Debian version of the VERSION file to the version of the
/// app (semver: "0.11.0~beta.1" becomes "0.11.0-beta.1").
#[allow(dead_code)]
pub fn semver_of(debian: &str) -> String {
    debian.trim().replace('~', "-")
}

#[cfg(test)]
mod tests {
    use super::*;

    fn args(l: &[&str]) -> Vec<String> {
        l.iter().map(|s| s.to_string()).collect()
    }

    #[test]
    fn handshake() {
        assert_eq!(
            parse_handshake("{\"agent\":1,\"port\":4567,\"version\":\"0.11.0\"}\n"),
            Some(Handshake { agent: 1, port: 4567, version: "0.11.0".into() })
        );
        assert_eq!(parse_handshake("{\"agent\":2,\"port\":4567}"), None);
        assert_eq!(parse_handshake("{\"agent\":1,\"port\":0}"), None);
        assert_eq!(parse_handshake("2026/11/05 log line"), None);
    }

    #[test]
    fn token() {
        let a = new_token();
        assert_eq!(a.len(), 48);
        assert_ne!(a, new_token());
        assert!(a.chars().all(|c| c.is_ascii_hexdigit()));
    }

    #[test]
    fn actions() {
        let got = actions_from_args(args(&[
            "strataos-desktop",
            "--share-link",
            "/home/a/Docs/x y.txt",
            "--background",
            "--open-on-nas",
            "C:\\Users\\a\\Docs",
            "strataos://pair?url=https%3A%2F%2Fnas&code=1",
            "--share-link",
        ]));
        assert_eq!(
            got,
            vec![
                Action::ShareLink { path: "/home/a/Docs/x y.txt".into() },
                Action::OpenOnNas { path: "C:\\Users\\a\\Docs".into() },
                Action::DeepLink { url: "strataos://pair?url=https%3A%2F%2Fnas&code=1".into() },
            ]
        );
        assert!(actions_from_args(args(&["x", "strataos://pair"])).len() == 1);
        assert!(actions_from_args(args(&["strataos://pair"])).is_empty()); // the program name
        assert!(is_background(args(&["x", "--background"])));
        assert!(!is_background(args(&["x"])));
    }

    #[test]
    fn json_of_actions() {
        let v = serde_json::to_string(&Action::OpenOnNas { path: "/a".into() }).unwrap();
        assert_eq!(v, "{\"kind\":\"openOnNas\",\"path\":\"/a\"}");
    }

    #[test]
    fn web_windows() {
        assert!(web_window_allowed("http://127.0.0.1:5123/__strataos/enter?k=ab"));
        assert!(!web_window_allowed("https://127.0.0.1:5123/"));
        assert!(!web_window_allowed("http://127.0.0.1/"));
        assert!(!web_window_allowed("http://localhost:5123/"));
        assert!(!web_window_allowed("http://127.0.0.1.evil.example:80/"));
        assert!(!web_window_allowed("http://user@127.0.0.1:5123/"));
        assert!(!web_window_allowed("javascript:alert(1)"));
    }

    #[test]
    fn tray_and_version() {
        assert_eq!(tray_icon_name("syncing"), "syncing");
        assert_eq!(tray_icon_name("whatever"), "idle");
        assert_eq!(semver_of("0.11.0~beta.1\n"), "0.11.0-beta.1");
    }
}
