/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.0.0
 */
use reqwest::{Client, Url};
use serde::{de::DeserializeOwned, Deserialize, Serialize};
use thiserror::Error;
use url::Host;

const EXPECTED_SERVICE: &str = "claude-code-monitor";

#[derive(Clone, Debug, Deserialize, PartialEq, Eq, Serialize)]
pub struct ConnectionSettings {
    pub host: String,
    pub port: u16,
}

impl Default for ConnectionSettings {
    fn default() -> Self {
        Self {
            host: "127.0.0.1".to_owned(),
            port: 9_100,
        }
    }
}

#[derive(Clone, Copy, Debug, Deserialize, PartialEq, Eq, Serialize)]
#[serde(rename_all = "lowercase")]
pub enum UsageRange {
    Day,
    Week,
    Month,
}

impl UsageRange {
    fn as_query_value(self) -> &'static str {
        match self {
            Self::Day => "day",
            Self::Week => "week",
            Self::Month => "month",
        }
    }
}

#[derive(Clone, Copy, Debug, Deserialize, PartialEq, Eq, Serialize)]
#[serde(rename_all = "lowercase")]
pub enum TelemetryClient {
    All,
    Claude,
    Codex,
}

impl TelemetryClient {
    fn as_query_value(self) -> &'static str {
        match self {
            Self::All => "all",
            Self::Claude => "claude",
            Self::Codex => "codex",
        }
    }
}

#[derive(Debug, Deserialize, Serialize)]
pub struct DashboardPayload {
    pub snapshot: SnapshotResponse,
    pub models: PeriodModelsResponse,
    pub version: Option<String>,
}

#[derive(Debug, Deserialize, Serialize)]
pub struct ConnectionCheck {
    pub version: Option<String>,
}

#[derive(Debug, Deserialize, Serialize)]
pub struct SnapshotResponse {
    pub updated_at: String,
    pub range: UsageRange,
    pub tokens: TokensBlock,
    pub cost: CostBlock,
    pub cache: CacheBlock,
    pub requests: RequestsBlock,
}

#[derive(Debug, Deserialize, Serialize)]
pub struct TokensBlock {
    #[serde(rename = "in")]
    pub input: i64,
    #[serde(rename = "out")]
    pub output: i64,
    pub total: i64,
    pub prev_total: i64,
    pub sparkline: Vec<i64>,
}

#[derive(Debug, Deserialize, Serialize)]
pub struct CostBlock {
    pub total: f64,
    pub prev_total: f64,
    pub sparkline: Vec<f64>,
    pub cost_estimated: bool,
}

#[derive(Debug, Deserialize, Serialize)]
pub struct CacheBlock {
    pub hit_rate: Option<f64>,
    pub read_tokens: i64,
    pub creation_tokens: i64,
}

#[derive(Debug, Deserialize, Serialize)]
pub struct RequestsBlock {
    pub total: i64,
    pub prev_total: i64,
    pub sparkline: Vec<i64>,
}

#[derive(Debug, Deserialize, Serialize)]
pub struct PeriodModelsResponse {
    pub updated_at: String,
    pub range: UsageRange,
    pub client: TelemetryClient,
    pub cost_estimated: bool,
    pub models: Vec<PeriodModelBlock>,
    #[serde(default = "models_available")]
    pub available: bool,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
pub struct PeriodModelBlock {
    pub model: String,
    pub requests: i64,
    pub input_tokens: i64,
    pub output_tokens: i64,
    pub total_tokens: i64,
    pub cost_usd: f64,
    pub share: f64,
}

#[derive(Debug, Deserialize)]
struct VersionResponse {
    service: String,
    version: String,
}

const fn models_available() -> bool {
    true
}

#[derive(Debug, Error)]
pub enum MonitorClientError {
    #[error("invalid host: {0}")]
    InvalidHost(String),
    #[error("port must be greater than zero")]
    InvalidPort,
    #[error("could not create HTTP client: {0}")]
    CreateClient(#[source] reqwest::Error),
    #[error("request to {path} failed: {source}")]
    Request {
        path: String,
        #[source]
        source: reqwest::Error,
    },
    #[error("{path} returned HTTP {status}")]
    HttpStatus { path: String, status: u16 },
    #[error("invalid JSON from {path}: {source}")]
    InvalidResponse {
        path: String,
        #[source]
        source: reqwest::Error,
    },
}

pub struct MonitorClient {
    base_url: Url,
    http: Client,
}

impl MonitorClient {
    pub fn new(settings: ConnectionSettings) -> Result<Self, MonitorClientError> {
        let host = parse_host(&settings.host)?;
        if settings.port == 0 {
            return Err(MonitorClientError::InvalidPort);
        }
        let base_url = Url::parse(&format!("http://{host}:{}/", settings.port))
            .map_err(|_| MonitorClientError::InvalidHost(settings.host.clone()))?;
        let http = Client::builder()
            .no_proxy()
            .timeout(std::time::Duration::from_secs(5))
            .build()
            .map_err(MonitorClientError::CreateClient)?;
        Ok(Self { base_url, http })
    }

    pub async fn fetch_dashboard(
        &self,
        range: UsageRange,
        client: TelemetryClient,
    ) -> Result<DashboardPayload, MonitorClientError> {
        let query = [
            ("range", range.as_query_value()),
            ("client", client.as_query_value()),
        ];
        let snapshot: SnapshotResponse = self.get_json("api/usage/snapshot", &query).await?;
        let models = match self.get_json("api/usage/models", &query).await {
            Ok(models) => models,
            Err(MonitorClientError::HttpStatus { status: 404, .. }) => PeriodModelsResponse {
                updated_at: snapshot.updated_at.clone(),
                range,
                client,
                cost_estimated: false,
                models: Vec::new(),
                available: false,
            },
            Err(error) => return Err(error),
        };
        let version = self.fetch_version().await;

        Ok(DashboardPayload {
            snapshot,
            models,
            version,
        })
    }

    pub async fn check_connection(&self) -> Result<ConnectionCheck, MonitorClientError> {
        let path = "internal/healthz";
        let url = self
            .base_url
            .join(path)
            .map_err(|_| MonitorClientError::InvalidHost(self.base_url.to_string()))?;
        let response =
            self.http
                .get(url)
                .send()
                .await
                .map_err(|source| MonitorClientError::Request {
                    path: path.to_owned(),
                    source,
                })?;
        let status = response.status();
        if !status.is_success() {
            return Err(MonitorClientError::HttpStatus {
                path: path.to_owned(),
                status: status.as_u16(),
            });
        }
        Ok(ConnectionCheck {
            version: self.fetch_version().await,
        })
    }

    async fn fetch_version(&self) -> Option<String> {
        let response: VersionResponse = self.get_json("version", &[]).await.ok()?;
        (response.service == EXPECTED_SERVICE && !response.version.trim().is_empty())
            .then_some(response.version)
    }

    async fn get_json<T: DeserializeOwned>(
        &self,
        path: &str,
        query: &[(&str, &str)],
    ) -> Result<T, MonitorClientError> {
        let mut url = self
            .base_url
            .join(path)
            .map_err(|_| MonitorClientError::InvalidHost(self.base_url.to_string()))?;
        if !query.is_empty() {
            url.query_pairs_mut().extend_pairs(query.iter().copied());
        }
        let response =
            self.http
                .get(url)
                .send()
                .await
                .map_err(|source| MonitorClientError::Request {
                    path: path.to_owned(),
                    source,
                })?;
        let status = response.status();
        if !status.is_success() {
            return Err(MonitorClientError::HttpStatus {
                path: path.to_owned(),
                status: status.as_u16(),
            });
        }
        response
            .json()
            .await
            .map_err(|source| MonitorClientError::InvalidResponse {
                path: path.to_owned(),
                source,
            })
    }
}

fn parse_host(raw: &str) -> Result<Host<String>, MonitorClientError> {
    if raw.is_empty() || raw.trim() != raw || raw.contains(['/', ':', '@']) {
        return Err(MonitorClientError::InvalidHost(raw.to_owned()));
    }
    Host::parse(raw).map_err(|_| MonitorClientError::InvalidHost(raw.to_owned()))
}
