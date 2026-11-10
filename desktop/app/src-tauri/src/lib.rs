//! The StrataOS desktop app: a native window (React, in ../src), its tray
//! icon, and the sync engine run as a sidecar (agent.rs). The window talks
//! to the engine and, through it, to the NAS; this shell only does what a
//! webview cannot: the tray, windows, start at login, file manager menus,
//! single instance and pairing links.

mod agent;
mod integration;
mod logic;

use std::sync::{Arc, Mutex};

use serde::Deserialize;
use tauri::image::Image;
use tauri::menu::{Menu, MenuItem, PredefinedMenuItem};
use tauri::tray::{MouseButton, MouseButtonState, TrayIcon, TrayIconBuilder, TrayIconEvent};
use tauri::{AppHandle, Emitter, Manager, RunEvent, WebviewUrl, WebviewWindowBuilder, WindowEvent};
use tauri_plugin_deep_link::DeepLinkExt;

use agent::{AgentInfo, AgentState};
use logic::Action;

/// Actions received before the window was ready to take them.
#[derive(Default)]
struct Pending(Mutex<Vec<Action>>);

struct Tray {
    icon: TrayIcon,
    status: MenuItem<tauri::Wry>,
    pause: MenuItem<tauri::Wry>,
    sync: MenuItem<tauri::Wry>,
    web: MenuItem<tauri::Wry>,
    open: MenuItem<tauri::Wry>,
    quit: MenuItem<tauri::Wry>,
}

struct TrayHolder(Mutex<Option<Tray>>);

fn tray_png(name: &str) -> Image<'static> {
    let bytes: &'static [u8] = match name {
        "syncing" => include_bytes!("../icons/tray-syncing.png"),
        "error" => include_bytes!("../icons/tray-error.png"),
        "paused" => include_bytes!("../icons/tray-paused.png"),
        _ => include_bytes!("../icons/tray-idle.png"),
    };
    Image::from_bytes(bytes).expect("tray icon")
}

fn show_main(app: &AppHandle) {
    if let Some(w) = app.get_webview_window("main") {
        let _ = w.unminimize();
        let _ = w.show();
        let _ = w.set_focus();
    }
}

/// Hands actions to the window (now, and kept for when it loads).
fn dispatch(app: &AppHandle, actions: Vec<Action>) {
    if actions.is_empty() {
        return;
    }
    let show = actions
        .iter()
        .any(|a| !matches!(a, Action::ShareLink { .. }));
    app.state::<Pending>()
        .0
        .lock()
        .unwrap()
        .extend(actions.iter().cloned());
    let _ = app.emit("actions", ());
    if show {
        show_main(app);
    }
}

#[tauri::command]
fn agent_info(state: tauri::State<'_, Arc<AgentState>>) -> Result<AgentInfo, String> {
    if let Some(i) = state.info.lock().unwrap().clone() {
        return Ok(i);
    }
    Err(state
        .error
        .lock()
        .unwrap()
        .clone()
        .unwrap_or_else(|| "starting".into()))
}

#[tauri::command]
fn take_actions(pending: tauri::State<'_, Pending>) -> Vec<Action> {
    std::mem::take(&mut *pending.0.lock().unwrap())
}

#[tauri::command]
fn started_in_background() -> bool {
    logic::is_background(std::env::args())
}

#[derive(Deserialize)]
struct TrayTexts {
    state: String,
    status: String,
    paused: bool,
    pause: String,
    resume: String,
    sync: String,
    web: String,
    open: String,
    quit: String,
    paired: bool,
}

#[tauri::command]
fn tray_update(holder: tauri::State<'_, TrayHolder>, t: TrayTexts) -> Result<(), String> {
    let g = holder.0.lock().unwrap();
    let Some(tr) = g.as_ref() else { return Ok(()) };
    let _ = tr
        .icon
        .set_icon(Some(tray_png(logic::tray_icon_name(&t.state))));
    let _ = tr
        .icon
        .set_tooltip(Some(format!("StrataOS · {}", t.status)));
    let _ = tr.status.set_text(&t.status);
    let _ = tr
        .pause
        .set_text(if t.paused { &t.resume } else { &t.pause });
    let _ = tr.pause.set_enabled(t.paired);
    let _ = tr.sync.set_text(&t.sync);
    let _ = tr.sync.set_enabled(t.paired && !t.paused);
    let _ = tr.web.set_text(&t.web);
    let _ = tr.web.set_enabled(t.paired);
    let _ = tr.open.set_text(&t.open);
    let _ = tr.quit.set_text(&t.quit);
    Ok(())
}

/// Opens the NAS web interface (through the agent's proxy) in a window.
#[tauri::command]
async fn open_web_window(
    app: AppHandle,
    url: String,
    title: String,
    key: String,
) -> Result<(), String> {
    if !logic::web_window_allowed(&url) {
        return Err("address refused".into());
    }
    let label: String = format!(
        "web-{}",
        key.chars()
            .filter(|c| c.is_ascii_alphanumeric())
            .take(32)
            .collect::<String>()
    );
    if let Some(w) = app.get_webview_window(&label) {
        let _ = w.navigate(url.parse().map_err(|_| "invalid address")?);
        let _ = w.set_focus();
        return Ok(());
    }
    WebviewWindowBuilder::new(
        &app,
        label,
        WebviewUrl::External(url.parse().map_err(|_| "invalid address")?),
    )
    .title(title)
    .inner_size(1280.0, 820.0)
    .min_inner_size(640.0, 420.0)
    .build()
    .map_err(|e| e.to_string())?;
    Ok(())
}

#[tauri::command]
fn integration_status() -> integration::Status {
    integration::status()
}

#[tauri::command]
fn integration_set(on: bool, labels: integration::Labels) -> Result<(), String> {
    integration::set(on, &labels)
}

/// Writes a text chosen by the user to a file chosen in the save dialog
/// (recovery keys).
#[tauri::command]
fn save_text(path: String, text: String) -> Result<(), String> {
    std::fs::write(&path, text).map_err(|e| e.to_string())
}

/// Writes a file fetched by the window (an earlier version of a NAS file)
/// to the path chosen in the save dialog.
#[tauri::command]
fn save_bytes(path: String, data: Vec<u8>) -> Result<(), String> {
    std::fs::write(&path, data).map_err(|e| e.to_string())
}

#[tauri::command]
fn quit(app: AppHandle) {
    app.exit(0);
}

fn build_tray(app: &AppHandle) -> tauri::Result<Tray> {
    let status = MenuItem::with_id(app, "status", "StrataOS", false, None::<&str>)?;
    let open = MenuItem::with_id(app, "open", "Open StrataOS", true, None::<&str>)?;
    let pause = MenuItem::with_id(app, "pause", "Pause", false, None::<&str>)?;
    let sync = MenuItem::with_id(app, "sync", "Sync now", false, None::<&str>)?;
    let web = MenuItem::with_id(
        app,
        "web",
        "Open the NAS web interface",
        false,
        None::<&str>,
    )?;
    let quit = MenuItem::with_id(app, "quit", "Quit", true, None::<&str>)?;
    let sep1 = PredefinedMenuItem::separator(app)?;
    let sep2 = PredefinedMenuItem::separator(app)?;
    let menu = Menu::with_items(
        app,
        &[&status, &sep1, &open, &pause, &sync, &web, &sep2, &quit],
    )?;
    let icon = TrayIconBuilder::with_id("main")
        .icon(tray_png("idle"))
        .tooltip("StrataOS")
        .menu(&menu)
        .show_menu_on_left_click(cfg!(target_os = "macos"))
        .on_menu_event(|app, ev| match ev.id().as_ref() {
            "open" => show_main(app),
            "quit" => app.exit(0),
            id => {
                // Pause, sync, web interface: the window knows the accounts.
                let _ = app.emit("tray", id.to_string());
                if id == "web" {
                    show_main(app);
                }
            }
        })
        .on_tray_icon_event(|tray, ev| {
            if let TrayIconEvent::Click {
                button: MouseButton::Left,
                button_state: MouseButtonState::Up,
                ..
            } = ev
            {
                if !cfg!(target_os = "macos") {
                    show_main(tray.app_handle());
                }
            }
        })
        .build(app)?;
    Ok(Tray {
        icon,
        status,
        pause,
        sync,
        web,
        open,
        quit,
    })
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    let state = Arc::new(AgentState::default());
    let app = tauri::Builder::default()
        // First: a second launch hands its arguments (menus of the file
        // manager, pairing links) to the running app and quits.
        .plugin(tauri_plugin_single_instance::init(|app, argv, _cwd| {
            let actions = logic::actions_from_args(argv);
            if actions.is_empty() {
                show_main(app);
            } else {
                dispatch(app, actions);
            }
        }))
        .plugin(tauri_plugin_deep_link::init())
        .plugin(tauri_plugin_notification::init())
        .plugin(tauri_plugin_dialog::init())
        .plugin(tauri_plugin_opener::init())
        .plugin(tauri_plugin_clipboard_manager::init())
        .plugin(tauri_plugin_process::init())
        .plugin(tauri_plugin_updater::Builder::new().build())
        .plugin(tauri_plugin_autostart::init(
            tauri_plugin_autostart::MacosLauncher::LaunchAgent,
            Some(vec!["--background"]),
        ))
        .manage(state.clone())
        .manage(Pending::default())
        .manage(TrayHolder(Mutex::new(None)))
        .invoke_handler(tauri::generate_handler![
            agent_info,
            take_actions,
            started_in_background,
            tray_update,
            open_web_window,
            integration_status,
            integration_set,
            save_text,
            save_bytes,
            quit
        ])
        .setup(|app| {
            let handle = app.handle().clone();
            agent::supervise(handle.clone());
            match build_tray(&handle) {
                Ok(t) => *app.state::<TrayHolder>().0.lock().unwrap() = Some(t),
                Err(e) => eprintln!("tray: {e}"), // some Linux desktops have no tray: the window stays
            }
            // Pairing links (strataos://pair?…) opened while running.
            let h = handle.clone();
            app.deep_link().on_open_url(move |ev| {
                let actions = ev
                    .urls()
                    .into_iter()
                    .map(|u| Action::DeepLink { url: u.to_string() })
                    .collect();
                dispatch(&h, actions);
            });
            #[cfg(any(target_os = "linux", all(debug_assertions, windows)))]
            {
                let _ = app.deep_link().register_all();
            }
            let mut first = logic::actions_from_args(std::env::args());
            if let Ok(Some(urls)) = app.deep_link().get_current() {
                for u in urls {
                    let a = Action::DeepLink { url: u.to_string() };
                    if !first.contains(&a) {
                        first.push(a);
                    }
                }
            }
            app.state::<Pending>().0.lock().unwrap().extend(first);
            let background = logic::is_background(std::env::args());
            let has_tray = app.state::<TrayHolder>().0.lock().unwrap().is_some();
            if !background || !has_tray {
                show_main(&handle);
            }
            Ok(())
        })
        .on_window_event(|w, ev| {
            // Closing the main window keeps the app in the tray.
            if let WindowEvent::CloseRequested { api, .. } = ev {
                if w.label() == "main"
                    && w.app_handle()
                        .state::<TrayHolder>()
                        .0
                        .lock()
                        .unwrap()
                        .is_some()
                {
                    api.prevent_close();
                    let _ = w.hide();
                }
            }
        })
        .build(tauri::generate_context!())
        .expect("error while building the app");
    app.run(move |handle, ev| match ev {
        RunEvent::ExitRequested { api, code, .. } => {
            // The last window closed: stay in the tray (quit comes from its menu).
            if code.is_none() && handle.state::<TrayHolder>().0.lock().unwrap().is_some() {
                api.prevent_exit();
            }
        }
        RunEvent::Exit => agent::stop(&state),
        #[cfg(target_os = "macos")]
        RunEvent::Reopen { .. } => show_main(handle),
        _ => {}
    });
}
