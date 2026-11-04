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

/// EgressService service trait
pub trait EgressService {
    type Context;
    /// list-flows (request/response).
    fn list_flows(
        &self,
        ctx: &Self::Context,
        input: ListFlowsRequest,
    ) -> Result<ListFlowsResponse, ServiceError>;
}
