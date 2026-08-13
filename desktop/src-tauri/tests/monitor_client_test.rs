/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.0.0
 */
use std::io::{Read, Write};
use std::net::{TcpListener, TcpStream};
use std::thread;

use vibecoding_monitor_lib::monitor_client::{
    ConnectionSettings, MonitorClient, TelemetryClient, UsageRange,
};

#[tokio::test]
async fn fetch_dashboard_combines_monitor_responses() {
    let (port, server) = spawn_monitor_server(3);
    let client = MonitorClient::new(ConnectionSettings {
        host: "127.0.0.1".to_owned(),
        port,
    })
    .expect("valid connection settings");

    let dashboard = client
        .fetch_dashboard(UsageRange::Day, TelemetryClient::All)
        .await
        .expect("dashboard response");

    assert_eq!(dashboard.snapshot.tokens.total, 12_840_000);
    assert_eq!(dashboard.snapshot.cost.total, 32.48);
    assert_eq!(dashboard.models.models.len(), 1);
    assert!(dashboard.models.available);
    assert_eq!(dashboard.models.models[0].model, "opus-4.8");
    assert_eq!(dashboard.version.as_deref(), Some("3.0"));
    server.join().expect("mock monitor server");
}

#[tokio::test]
async fn check_connection_uses_health_endpoint_and_reports_version() {
    let (port, server) = spawn_monitor_server(2);
    let client = MonitorClient::new(ConnectionSettings {
        host: "127.0.0.1".to_owned(),
        port,
    })
    .expect("valid connection settings");

    let check = client
        .check_connection()
        .await
        .expect("healthy monitor response");

    assert_eq!(check.version.as_deref(), Some("3.0"));
    server.join().expect("mock monitor server");
}

#[tokio::test]
async fn invalid_version_response_does_not_discard_dashboard_data() {
    let (port, server) = spawn_monitor_server_with_version(3, "not-json");
    let client = MonitorClient::new(ConnectionSettings {
        host: "127.0.0.1".to_owned(),
        port,
    })
    .expect("valid connection settings");

    let dashboard = client
        .fetch_dashboard(UsageRange::Day, TelemetryClient::All)
        .await
        .expect("business data remains available");

    assert_eq!(dashboard.snapshot.tokens.total, 12_840_000);
    assert_eq!(dashboard.version, None);
    server.join().expect("mock monitor server");
}

#[tokio::test]
async fn missing_period_models_endpoint_does_not_discard_snapshot_data() {
    let (port, server) = spawn_monitor_server_with_options(
        3,
        r#"{"service":"claude-code-monitor","version":"2.6.0"}"#,
        false,
    );
    let client = MonitorClient::new(ConnectionSettings {
        host: "127.0.0.1".to_owned(),
        port,
    })
    .expect("valid connection settings");

    let dashboard = client
        .fetch_dashboard(UsageRange::Week, TelemetryClient::Codex)
        .await
        .expect("snapshot remains available with an older monitor service");

    assert_eq!(dashboard.snapshot.tokens.total, 12_840_000);
    assert!(dashboard.models.models.is_empty());
    assert!(!dashboard.models.available);
    assert_eq!(dashboard.models.range, UsageRange::Week);
    assert_eq!(dashboard.models.client, TelemetryClient::Codex);
    assert_eq!(dashboard.version.as_deref(), Some("2.6.0"));
    server.join().expect("mock monitor server");
}

#[test]
fn connection_settings_reject_schemes_paths_and_zero_ports() {
    assert!(MonitorClient::new(ConnectionSettings {
        host: "http://127.0.0.1/path".to_owned(),
        port: 9_100,
    })
    .is_err());
    assert!(MonitorClient::new(ConnectionSettings {
        host: "127.0.0.1".to_owned(),
        port: 0,
    })
    .is_err());
}

fn spawn_monitor_server(expected_requests: usize) -> (u16, thread::JoinHandle<()>) {
    spawn_monitor_server_with_options(
        expected_requests,
        r#"{"service":"claude-code-monitor","version":"3.0"}"#,
        true,
    )
}

fn spawn_monitor_server_with_version(
    expected_requests: usize,
    version_body: &'static str,
) -> (u16, thread::JoinHandle<()>) {
    spawn_monitor_server_with_options(expected_requests, version_body, true)
}

fn spawn_monitor_server_with_options(
    expected_requests: usize,
    version_body: &'static str,
    models_available: bool,
) -> (u16, thread::JoinHandle<()>) {
    let listener = TcpListener::bind(("127.0.0.1", 0)).expect("bind test server");
    let port = listener.local_addr().expect("test server address").port();
    let server = thread::spawn(move || {
        for _ in 0..expected_requests {
            let (mut stream, _) = listener.accept().expect("accept request");
            respond(&mut stream, version_body, models_available);
        }
    });
    (port, server)
}

fn respond(stream: &mut TcpStream, version_body: &str, models_available: bool) {
    let mut request = [0_u8; 4096];
    let size = stream.read(&mut request).expect("read request");
    let request = String::from_utf8_lossy(&request[..size]);
    let path = request
        .lines()
        .next()
        .and_then(|line| line.split_whitespace().nth(1))
        .expect("request path");
    let (status, body) = if path == "/internal/healthz" {
        ("200 OK", "ok")
    } else if path.starts_with("/api/usage/snapshot") {
        (
            "200 OK",
            r#"{
          "updated_at":"2026-08-12T10:42:00Z",
          "range":"day",
          "tokens":{"in":9000000,"out":3840000,"total":12840000,"prev_total":10000000,"sparkline":[12840000]},
          "cost":{"total":32.48,"prev_total":28.10,"sparkline":[32.48],"cost_estimated":true},
          "cache":{"hit_rate":0.75,"read_tokens":3000000,"creation_tokens":1000000},
          "requests":{"total":42,"prev_total":36,"sparkline":[42]},
          "models":[]
        }"#,
        )
    } else if path.starts_with("/api/usage/models") && !models_available {
        ("404 Not Found", r#"{"error":"not found"}"#)
    } else if path.starts_with("/api/usage/models") {
        (
            "200 OK",
            r#"{
          "updated_at":"2026-08-12T10:42:00Z",
          "range":"day",
          "client":"all",
          "cost_estimated":true,
          "models":[{
            "model":"opus-4.8","requests":18,"input_tokens":4000000,
            "output_tokens":1620000,"total_tokens":5620000,"cost_usd":18.43,"share":0.438
          }]
        }"#,
        )
    } else if path == "/version" {
        ("200 OK", version_body)
    } else {
        panic!("unexpected request path: {path}");
    };
    let response = format!(
        "HTTP/1.1 {status}\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
        body.len(),
        body,
    );
    stream
        .write_all(response.as_bytes())
        .expect("write response");
}
