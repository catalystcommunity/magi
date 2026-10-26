//! Generated transport-agnostic service clients from CSIL specification

#![allow(async_fn_in_trait)]

use super::client::ClientError;
use super::codec::*;
use super::types::*;

/// The caller-supplied byte carrier: it performs the call named by `(service, op)`
/// with the already-encoded request bytes and returns the response bytes, or an
/// error. The generated client owns (de)serialization via the codec; the carrier
/// only moves bytes, so it can be HTTP, a queue, or an in-process loop.
pub trait AsyncTransport {
    async fn call(&self, service: &str, op: &str, req: &[u8]) -> Result<Vec<u8>, ClientError>;
}

/// Typed client for the Interaction service.
pub struct InteractionAsyncClient<T: AsyncTransport> {
    #[allow(dead_code)]
    transport: T,
}

impl<T: AsyncTransport> InteractionAsyncClient<T> {
    pub fn new(transport: T) -> Self {
        Self { transport }
    }

    /// submit-input (request/response).
    pub async fn submit_input(&self, req: SubmitInputRequest) -> Result<Receipt, ClientError> {
        let csil_resp = self
            .transport
            .call(
                "Interaction",
                "submit-input",
                &encode_submit_input_request(&req),
            )
            .await?;
        decode_receipt(&csil_resp).map_err(|e| ClientError::Transport(e.to_string()))
    }

    /// correct-association (request/response).
    pub async fn correct_association(
        &self,
        req: CorrectAssociationRequest,
    ) -> Result<Receipt, ClientError> {
        let csil_resp = self
            .transport
            .call(
                "Interaction",
                "correct-association",
                &encode_correct_association_request(&req),
            )
            .await?;
        decode_receipt(&csil_resp).map_err(|e| ClientError::Transport(e.to_string()))
    }
}

/// Typed client for the Evidence service.
pub struct EvidenceAsyncClient<T: AsyncTransport> {
    #[allow(dead_code)]
    transport: T,
}

impl<T: AsyncTransport> EvidenceAsyncClient<T> {
    pub fn new(transport: T) -> Self {
        Self { transport }
    }

    /// read-history (request/response).
    pub async fn read_history(&self, req: ReadHistoryRequest) -> Result<HistoryPage, ClientError> {
        let csil_resp = self
            .transport
            .call(
                "Evidence",
                "read-history",
                &encode_read_history_request(&req),
            )
            .await?;
        decode_history_page(&csil_resp).map_err(|e| ClientError::Transport(e.to_string()))
    }
}

/// Typed client for the Knowledge service.
pub struct KnowledgeAsyncClient<T: AsyncTransport> {
    #[allow(dead_code)]
    transport: T,
}

impl<T: AsyncTransport> KnowledgeAsyncClient<T> {
    pub fn new(transport: T) -> Self {
        Self { transport }
    }

    /// query (request/response).
    pub async fn query(&self, req: KnowledgeQuery) -> Result<QueryResult, ClientError> {
        let csil_resp = self
            .transport
            .call("Knowledge", "query", &encode_knowledge_query(&req))
            .await?;
        decode_query_result(&csil_resp).map_err(|e| ClientError::Transport(e.to_string()))
    }
}
