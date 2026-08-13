/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.0.0
 */
mod commands;
mod menu_bar;
pub mod monitor_client;

pub fn run() {
    tauri::Builder::default()
        .enable_macos_default_menu(false)
        .plugin(tauri_plugin_single_instance::init(|app, _args, _cwd| {
            menu_bar::show_panel(app);
        }))
        .plugin(tauri_plugin_positioner::init())
        .plugin(
            tauri_plugin_autostart::Builder::new()
                .macos_launcher(tauri_plugin_autostart::MacosLauncher::LaunchAgent)
                .build(),
        )
        .setup(menu_bar::setup)
        .on_window_event(menu_bar::handle_window_event)
        .invoke_handler(tauri::generate_handler![
            commands::fetch_dashboard,
            commands::check_connection,
            commands::hide_panel,
        ])
        .run(tauri::generate_context!())
        .expect("failed to run Vibecoding Monitor");
}
