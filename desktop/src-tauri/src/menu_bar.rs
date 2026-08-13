/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.0.0
 */
use tauri::image::Image;
use tauri::menu::MenuBuilder;
use tauri::tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent};
use tauri::{
    App, AppHandle, Emitter, LogicalSize, Manager, Monitor, PhysicalPosition, WebviewWindow,
    WindowEvent,
};
use tauri_plugin_positioner::{Position, WindowExt};

use std::sync::Mutex;

const MAIN_WINDOW: &str = "main";
const MENU_BAR_CLEARANCE: f64 = 24.0;
const SCREEN_BOTTOM_CLEARANCE: f64 = 12.0;
const MIN_PANEL_HEIGHT: f64 = 520.0;
const MAX_PANEL_HEIGHT: f64 = 800.0;

#[derive(Default)]
struct TrayAnchor(Mutex<Option<PhysicalPosition<f64>>>);

pub fn setup(app: &mut App) -> Result<(), Box<dyn std::error::Error>> {
    app.handle()
        .set_activation_policy(tauri::ActivationPolicy::Accessory)?;
    app.manage(TrayAnchor::default());

    let menu = MenuBuilder::new(app)
        .text("open", "打开面板")
        .text("refresh", "立即刷新")
        .text("settings", "设置…")
        .separator()
        .text("quit", "退出 Vibecoding Monitor")
        .build()?;
    let tray_icon = Image::from_bytes(include_bytes!("../icons/tray/44x44.png"))?;

    TrayIconBuilder::with_id("vibecoding-monitor")
        .icon(tray_icon)
        .icon_as_template(true)
        .tooltip("Vibecoding Monitor")
        .menu(&menu)
        .show_menu_on_left_click(false)
        .on_tray_icon_event(|tray, event| {
            tauri_plugin_positioner::on_tray_event(tray.app_handle(), &event);
            remember_tray_anchor(tray.app_handle(), &event);
            if matches!(
                event,
                TrayIconEvent::Click {
                    button: MouseButton::Left,
                    button_state: MouseButtonState::Up,
                    ..
                }
            ) {
                toggle_panel(tray.app_handle());
            }
        })
        .on_menu_event(|app, event| match event.id().as_ref() {
            "open" => show_panel(app),
            "refresh" => {
                show_panel(app);
                let _ = app.emit_to(MAIN_WINDOW, "refresh-requested", ());
            }
            "settings" => {
                show_panel(app);
                let _ = app.emit_to(MAIN_WINDOW, "settings-requested", ());
            }
            "quit" => app.exit(0),
            _ => {}
        })
        .build(app)?;

    Ok(())
}

pub fn show_panel(app: &AppHandle) {
    let Some(window) = app.get_webview_window(MAIN_WINDOW) else {
        return;
    };
    let monitor = panel_monitor(app, &window);
    if let Some(monitor) = &monitor {
        fit_panel_height(&window, monitor);
    }
    let _ = window.move_window_constrained(Position::TrayBottomCenter);
    if let Some(monitor) = &monitor {
        keep_inside_monitor(&window, monitor);
    }
    let _ = window.show();
    let _ = window.set_focus();
    let _ = window.emit("panel-shown", ());
}

fn remember_tray_anchor(app: &AppHandle, event: &TrayIconEvent) {
    let rect = match event {
        TrayIconEvent::Click { rect, .. }
        | TrayIconEvent::DoubleClick { rect, .. }
        | TrayIconEvent::Enter { rect, .. }
        | TrayIconEvent::Move { rect, .. }
        | TrayIconEvent::Leave { rect, .. } => rect,
        _ => return,
    };
    let position = rect.position.to_physical::<f64>(1.0);
    let size = rect.size.to_physical::<f64>(1.0);
    let center = PhysicalPosition::new(
        position.x + size.width / 2.0,
        position.y + size.height / 2.0,
    );
    if let Ok(mut anchor) = app.state::<TrayAnchor>().0.lock() {
        *anchor = Some(center);
    }
}

fn panel_monitor(app: &AppHandle, window: &WebviewWindow) -> Option<Monitor> {
    let anchor = app
        .state::<TrayAnchor>()
        .0
        .lock()
        .ok()
        .and_then(|anchor| *anchor);
    if let Some(anchor) = anchor {
        if let Ok(Some(monitor)) = app.monitor_from_point(anchor.x, anchor.y) {
            return Some(monitor);
        }
    }
    window
        .current_monitor()
        .ok()
        .flatten()
        .or_else(|| app.primary_monitor().ok().flatten())
}

fn fit_panel_height(window: &WebviewWindow, monitor: &Monitor) {
    let scale_factor = monitor.scale_factor();
    let monitor_top = monitor.position().y;
    let menu_bottom = monitor_top + (MENU_BAR_CLEARANCE * scale_factor).round() as i32;
    let work_area = monitor.work_area();
    let available_bottom = work_area.position.y
        + i32::try_from(work_area.size.height).unwrap_or(i32::MAX)
        - (SCREEN_BOTTOM_CLEARANCE * scale_factor).round() as i32;
    let available_height = f64::from((available_bottom - menu_bottom).max(1)) / scale_factor;
    let panel_height = constrained_panel_height(available_height);
    let _ = window.set_size(LogicalSize::new(464.0, panel_height));
}

fn constrained_panel_height(available_height: f64) -> f64 {
    let minimum = MIN_PANEL_HEIGHT.min(available_height);
    available_height.clamp(minimum, MAX_PANEL_HEIGHT)
}

fn keep_inside_monitor(window: &WebviewWindow, monitor: &Monitor) {
    let Ok(position) = window.outer_position() else {
        return;
    };
    let minimum_y =
        monitor.position().y + (MENU_BAR_CLEARANCE * monitor.scale_factor()).round() as i32;
    if position.y < minimum_y {
        let _ = window.set_position(PhysicalPosition::new(position.x, minimum_y));
    }
}

pub fn hide_panel_window(window: &WebviewWindow) -> tauri::Result<()> {
    window.emit("panel-hidden", ())?;
    window.hide()
}

fn toggle_panel(app: &AppHandle) {
    let Some(window) = app.get_webview_window(MAIN_WINDOW) else {
        return;
    };
    if window.is_visible().unwrap_or(false) {
        let _ = hide_panel_window(&window);
    } else {
        show_panel(app);
    }
}

pub fn handle_window_event(window: &tauri::Window, event: &WindowEvent) {
    if window.label() != MAIN_WINDOW {
        return;
    }
    match event {
        WindowEvent::Focused(false) => {
            let _ = window.emit("panel-hidden", ());
            let _ = window.hide();
        }
        WindowEvent::CloseRequested { api, .. } => {
            api.prevent_close();
            let _ = window.emit("panel-hidden", ());
            let _ = window.hide();
        }
        _ => {}
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn panel_height_shrinks_to_available_screen_space() {
        assert_eq!(constrained_panel_height(584.0), 584.0);
        assert_eq!(constrained_panel_height(900.0), MAX_PANEL_HEIGHT);
        assert_eq!(constrained_panel_height(480.0), 480.0);
    }
}
