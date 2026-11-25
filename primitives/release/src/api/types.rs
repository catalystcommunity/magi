//! Generated types from CSIL specification

#![allow(clippy::large_enum_variant)]

#[derive(Debug, Clone, PartialEq)]
pub struct Target {
    pub name: String,
    pub namespace: String,
    pub deployment: String,
    pub container: String,
    pub health_path: Option<String>,
    pub health_port: Option<u64>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct TargetImage {
    pub target: String,
    pub image: String,
}

#[derive(Debug, Clone, PartialEq)]
pub struct ReleaseItem {
    pub target: String,
    pub image: String,
    pub previous_image: String,
    pub status: String,
    pub message: Option<String>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct Release {
    pub id: String,
    pub owner: String,
    pub status: String,
    pub reason: Option<String>,
    pub items: Vec<ReleaseItem>,
    pub created_ms: u64,
    pub updated_ms: u64,
}

#[derive(Debug, Clone, PartialEq)]
pub struct CreateReleaseRequest {
    pub images: Vec<TargetImage>,
    pub reason: Option<String>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct GetReleaseRequest {
    pub id: String,
}

#[derive(Debug, Clone, PartialEq)]
pub struct ListReleasesRequest {
    pub limit: Option<u64>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct ListReleasesResponse {
    pub releases: Vec<Release>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct RollbackRequest {
    pub id: String,
}

#[derive(Debug, Clone, PartialEq)]
pub struct ListTargetsRequest {
    pub name: Option<String>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct ListTargetsResponse {
    pub targets: Vec<Target>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct Workload {
    pub name: String,
    pub kind: String,
    pub role: Option<String>,
    pub ready: bool,
    pub restarts: u64,
}

#[derive(Debug, Clone, PartialEq)]
pub struct Incident {
    pub seq: u64,
    pub kind: String,
    pub subject: String,
    pub message: String,
    pub at_ms: u64,
}

#[derive(Debug, Clone, PartialEq)]
pub struct HealthStatusRequest {
    pub kind: Option<String>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct HealthStatusResponse {
    pub watched: bool,
    pub workloads: Vec<Workload>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct ListIncidentsRequest {
    pub after: Option<u64>,
    pub limit: Option<u64>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct ListIncidentsResponse {
    pub incidents: Vec<Incident>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct PodFacts {
    pub name: String,
    pub phase: String,
    pub ready: bool,
    pub restarts: u64,
    pub state: String,
    pub last_exit: Option<String>,
    pub log: String,
    pub events: Vec<String>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct DiagnoseRequest {
    pub subject: String,
}

#[derive(Debug, Clone, PartialEq)]
pub struct Diagnosis {
    pub subject: String,
    pub pods: Vec<PodFacts>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct RestartWorkloadRequest {
    pub subject: String,
    pub reason: Option<String>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct RestartWorkloadResponse {
    pub restarted: Vec<String>,
}
