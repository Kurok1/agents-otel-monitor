/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.0.0
 */
use tauri::WebviewWindow;

use crate::menu_bar::hide_panel_window;
use crate::monitor_client::{
    ConnectionCheck, ConnectionSettings, DashboardPayload, MonitorClient, TelemetryClient,
    UsageRange,
};

#[tauri::command]
pub async fn fetch_dashboard(
    settings: ConnectionSettings,
    range: UsageRange,
    client: TelemetryClient,
) -> Result<DashboardPayload, String> {
    let monitor = MonitorClient::new(settings).map_err(|error| error.to_string())?;
    monitor
        .fetch_dashboard(range, client)
        .await
        .map_err(|error| error.to_string())
}

#[tauri::command]
pub async fn check_connection(settings: ConnectionSettings) -> Result<ConnectionCheck, String> {
    let monitor = MonitorClient::new(settings).map_err(|error| error.to_string())?;
    monitor
        .check_connection()
        .await
        .map_err(|error| error.to_string())
}

#[tauri::command]
pub fn hide_panel(window: WebviewWindow) -> Result<(), String> {
    hide_panel_window(&window).map_err(|error| error.to_string())
}
