//! Runs the sync engine (`strataos-sync agent`, the Go sidecar) and keeps
//! it running: a new token at each start, its port read from its first
//! line, a restart with a pause when it stops. Its standard input stays
//! open: when this app ends, even killed, the agent sees it close and quits.

use std::io::{BufRead, BufReader, Read};
use std::path::PathBuf;
use std::process::{Child, Command, Stdio};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use serde::Serialize;
use tauri::{AppHandle, Emitter, Manager};

use crate::logic;

#[derive(Clone, Serialize, Debug)]
pub struct AgentInfo {
    pub url: String,
    pub token: String,
    pub version: String,
}

#[derive(Default)]
pub struct AgentState {
    pub info: Mutex<Option<AgentInfo>>,
    pub child: Mutex<Option<Child>>,
    pub error: Mutex<Option<String>>,
    pub stopping: Mutex<bool>,
}

/// The sidecar's path: next to this program (Tauri puts it there, without
/// its target triple, in every bundle and in development builds).
fn sidecar_path() -> PathBuf {
    let exe = std::env::current_exe().unwrap_or_else(|_| PathBuf::from("."));
    let dir = exe.parent().map(PathBuf::from).unwrap_or_default();
    dir.join(format!("strataos-sync{}", std::env::consts::EXE_SUFFIX))
}

/// Folder of the settings (the same as the former app and the headless mode).
fn config_dir() -> Option<PathBuf> {
    if let Ok(d) = std::env::var("STRATAOS_CONFIG_DIR") {
        if !d.is_empty() {
            return Some(PathBuf::from(d));
        }
    }
    let base = if cfg!(target_os = "windows") {
        std::env::var_os("APPDATA").map(PathBuf::from)
    } else if cfg!(target_os = "macos") {
        std::env::var_os("HOME").map(|h| PathBuf::from(h).join("Library/Application Support"))
    } else {
        std::env::var_os("XDG_CONFIG_HOME")
            .filter(|v| !v.is_empty())
            .map(PathBuf::from)
            .or_else(|| std::env::var_os("HOME").map(|h| PathBuf::from(h).join(".config")))
    };
    base.map(|b| b.join("StrataOS Sync"))
}

fn spawn() -> Result<(Child, AgentInfo), String> {
    let token = logic::new_token();
    let mut cmd = Command::new(sidecar_path());
    if let Some(d) = config_dir() {
        cmd.arg("--config").arg(d);
    }
    cmd.arg("agent")
        .env("STRATAOS_AGENT_TOKEN", &token)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    if cfg!(debug_assertions) {
        cmd.env("STRATAOS_AGENT_DEV", "1");
    }
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        cmd.creation_flags(0x0800_0000); // CREATE_NO_WINDOW
    }
    let mut child = cmd.spawn().map_err(|e| format!("cannot start {}: {e}", sidecar_path().display()))?;
    let stdout = child.stdout.take().ok_or("no output")?;
    let stderr = child.stderr.take();
    let (tx, rx) = std::sync::mpsc::channel();
    std::thread::spawn(move || {
        let mut r = BufReader::new(stdout);
        let mut line = String::new();
        let mut sent = false;
        loop {
            line.clear();
            match r.read_line(&mut line) {
                Ok(0) | Err(_) => break,
                Ok(_) => {
                    if !sent {
                        if let Some(h) = logic::parse_handshake(&line) {
                            let _ = tx.send(h);
                            sent = true;
                        }
                    }
                }
            }
        }
    });
    if let Some(mut e) = stderr {
        // The agent logs to its file too: drain its error output so that it never blocks.
        std::thread::spawn(move || {
            let mut buf = [0u8; 8192];
            while let Ok(n) = e.read(&mut buf) {
                if n == 0 {
                    break;
                }
                if cfg!(debug_assertions) {
                    eprint!("{}", String::from_utf8_lossy(&buf[..n]));
                }
            }
        });
    }
    match rx.recv_timeout(Duration::from_secs(30)) {
        Ok(h) => {
            let info = AgentInfo { url: format!("http://127.0.0.1:{}", h.port), token, version: h.version };
            Ok((child, info))
        }
        Err(_) => {
            let _ = child.kill();
            let status = child.wait().ok().map(|s| s.to_string()).unwrap_or_default();
            Err(format!("the sync engine did not start ({status})"))
        }
    }
}

/// Starts the agent and restarts it whenever it stops.
pub fn supervise(app: AppHandle) {
    std::thread::spawn(move || {
        let state: Arc<AgentState> = app.state::<Arc<AgentState>>().inner().clone();
        let mut delay = Duration::from_secs(1);
        loop {
            if *state.stopping.lock().unwrap() {
                return;
            }
            match spawn() {
                Ok((child, info)) => {
                    *state.error.lock().unwrap() = None;
                    *state.info.lock().unwrap() = Some(info.clone());
                    *state.child.lock().unwrap() = Some(child);
                    let _ = app.emit("agent", info);
                    delay = Duration::from_secs(1);
                    // Wait for it to stop.
                    loop {
                        std::thread::sleep(Duration::from_millis(500));
                        let mut g = state.child.lock().unwrap();
                        match g.as_mut().map(|c| c.try_wait()) {
                            Some(Ok(None)) => continue,
                            _ => {
                                *g = None;
                                break;
                            }
                        }
                    }
                    *state.info.lock().unwrap() = None;
                }
                Err(e) => {
                    eprintln!("{e}");
                    *state.error.lock().unwrap() = Some(e.clone());
                    let _ = app.emit("agent-error", e);
                }
            }
            if *state.stopping.lock().unwrap() {
                return;
            }
            std::thread::sleep(delay);
            delay = (delay * 2).min(Duration::from_secs(30));
        }
    });
}

/// Stops the agent (quitting the app).
pub fn stop(state: &AgentState) {
    *state.stopping.lock().unwrap() = true;
    if let Some(mut c) = state.child.lock().unwrap().take() {
        drop(c.stdin.take()); // the agent quits cleanly when its input closes
        for _ in 0..50 {
            if let Ok(Some(_)) = c.try_wait() {
                return;
            }
            std::thread::sleep(Duration::from_millis(100));
        }
        let _ = c.kill();
        let _ = c.wait();
    }
}
