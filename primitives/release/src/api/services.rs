//! Generated service traits from CSIL specification

use super::types::*;

#[derive(Debug, Clone)]
pub struct ServiceError {
    pub code: i32,
    pub message: String,
}

impl std::fmt::Display for ServiceError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "service error {}: {}", self.code, self.message)
    }
}

impl std::error::Error for ServiceError {}

/// ReleaseService service trait
pub trait ReleaseService {
    type Context;
    /// create (request/response).
    fn create(
        &self,
        ctx: &Self::Context,
        input: CreateReleaseRequest,
    ) -> Result<Release, ServiceError>;
    /// get (request/response).
    fn get(&self, ctx: &Self::Context, input: GetReleaseRequest) -> Result<Release, ServiceError>;
    /// list (request/response).
    fn list(
        &self,
        ctx: &Self::Context,
        input: ListReleasesRequest,
    ) -> Result<ListReleasesResponse, ServiceError>;
    /// rollback (request/response).
    fn rollback(
        &self,
        ctx: &Self::Context,
        input: RollbackRequest,
    ) -> Result<Release, ServiceError>;
    /// list-targets (request/response).
    fn list_targets(
        &self,
        ctx: &Self::Context,
        input: ListTargetsRequest,
    ) -> Result<ListTargetsResponse, ServiceError>;
}
