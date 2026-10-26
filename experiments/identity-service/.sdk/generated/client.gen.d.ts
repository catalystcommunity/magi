import type { ActivateUserRequest, ActivateUserResponse, AddApplicationKeyRequest, AddApplicationKeyResponse, AddTrustedIssuerRequest, AddTrustedIssuerResponse, AdminIssueAttestationRequest, AdminIssueAttestationResponse, AdminUserClaimsRequest, AdminUserClaimsResponse, ApproveClaimRequest, ApproveClaimResponse, ApproveLocalRpRequest, ApproveLocalRpResponse, AuthenticateRequest, AuthenticateResponse, BrowserActAsCompleteRequest, BrowserActAsCompleteResponse, BrowserActAsInspectRequest, BrowserActAsInspectResponse, BrowserAuthorizationCompleteRequest, BrowserAuthorizationCompleteResponse, BrowserAuthorizationInspectRequest, BrowserAuthorizationInspectResponse, ChangePasswordRequest, ChangePasswordResponse, CheckPermissionRequest, CheckPermissionResponse, CheckResult, CompletePasswordRecoveryRequest, CompletePasswordRecoveryResponse, ConfirmContactVerificationRequest, ConfirmContactVerificationResponse, CreateGuestbookRequest, CreateProfileRequest, CreateProfileResponse, CreateUserRequest, CreateUserResponse, DeactivateUserRequest, DeactivateUserResponse, DeleteGuestbookRequest, DeleteGuestbookResponse, DenyLocalRpRequest, DenyLocalRpResponse, DepositClaimRequest, DepositClaimResponse, EmptyRequest, EnrollApplicationInstanceRequest, EnrollApplicationInstanceResponse, GetActAsGrantRevocationsRequest, GetActAsGrantRevocationsResponse, GetApplicationKeysRequest, GetApplicationKeysResponse, GetDomainKeysResponse, GetLocalRpPolicyRequest, GetLocalRpPolicyResponse, GetLocalRpRequest, GetLocalRpResponse, GetMyInfoResponse, GetNotificationCapabilitiesResponse, GetRevocationsRequest, GetRevocationsResponse, GetUiConfigurationResponse, GetUserAuthenticationActivityRequest, GetUserAuthenticationActivityResponse, GetUserKeysRequest, GetUserKeysResponse, GetUserRequest, GetUserResponse, GrantRelationRequest, GrantRelationResponse, GuestbookEntry, GuestbookListRequest, GuestbookListResponse, HandshakeRequest, HandshakeResponse, HelloRequest, HelloResponse, IntrospectBrowserSessionRequest, IntrospectBrowserSessionResponse, ListActAsGrantsResponse, ListClaimTypesResponse, ListLocalRpsRequest, ListLocalRpsResponse, ListLocalesResponse, ListPendingClaimApprovalsResponse, ListRelationsRequest, ListRelationsResponse, ListReleaseRulesResponse, ListSettablePoliciesResponse, ListTrustedIssuersResponse, ListUserClaimsRequest, ListUserClaimsResponse, ListUsersRequest, ListUsersResponse, ListVerifiedContactMethodsResponse, LocalRpTicketRedemptionResponse, PurgeLocalRpTicketsRequest, PurgeLocalRpTicketsResponse, PurgeUserRequest, PurgeUserResponse, RecheckPinsRequest, RecheckPinsResponse, RefreshActAsGrantRequest, RefreshActAsGrantResponse, RejectClaimRequest, RejectClaimResponse, RemoveClaimRequest, RemoveClaimResponse, RemoveClaimTypeLabelRequest, RemoveClaimTypeLabelResponse, RemoveClaimTypeRequest, RemoveClaimTypeResponse, RemoveCredentialRequest, RemoveCredentialResponse, RemoveMyClaimRequest, RemoveMyClaimResponse, RemoveRelationRequest, RemoveRelationResponse, RemoveReleaseRuleRequest, RemoveReleaseRuleResponse, RemoveTrustedIssuerRequest, RemoveTrustedIssuerResponse, RenewApplicationKeyAttestationRequest, RenewApplicationKeyAttestationResponse, RequestContactVerificationRequest, RequestContactVerificationResponse, RequestPasswordRecoveryRequest, RequestPasswordRecoveryResponse, RequestVerificationRequest, RequestVerificationResponse, ResetPasswordRequest, ResetPasswordResponse, RevokeActAsGrantRequest, RevokeActAsGrantResponse, RevokeApplicationKeyRequest, RevokeApplicationKeyResponse, RevokeDomainKeyRequest, RevokeDomainKeyResponse, RevokeLocalRpRequest, RevokeLocalRpResponse, RevokeVerifiedContactMethodRequest, RevokeVerifiedContactMethodResponse, RpActAsRefreshRequest, RpDecryptRequest, RpDecryptResponse, RpIssueAttestationRequest, RpIssueAttestationResponse, RpResolveActAsRevocationsRequest, RpResolveApplicationKeysRequest, RpResolveApplicationKeysResponse, RpResolveDomainKeysRequest, RpResolveDomainKeysResponse, RpSignRequest, RpSignResponse, RpUserInfoRequest, RpVerifyRequest, RpVerifyResponse, SessionCurrentResponse, SessionLogoutResponse, SessionPasswordLoginRequest, SessionPasswordLoginResponse, SetClaimRequest, SetClaimResponse, SetClaimTypeLabelRequest, SetClaimTypeLabelResponse, SetClaimTypeRequest, SetClaimTypeResponse, SetLocalRpPolicyRequest, SetLocalRpPolicyResponse, SetMyClaimRequest, SetMyClaimResponse, SetMyClaimSharingRequest, SetMyClaimSharingResponse, SetReleaseRuleRequest, SetReleaseRuleResponse, SetUserClaimRequest, SetUserClaimResponse, SignedLocalRpTicketRedemptionRequest, SignedUserInfoRequest, StartApplicationKeyChallengeRequest, StartApplicationKeyChallengeResponse, TranslationsRequest, TranslationsResponse, UpdateGuestbookRequest, UpdateUserRequest, UpdateUserResponse, UserInfo, ValidatePasswordRecoveryRequest, ValidatePasswordRecoveryResponse } from "./types.gen.ts";
export interface ServiceTransport {
    call(service: string, op: string, req: Uint8Array): Uint8Array;
}
export declare class AccountClient {
    private readonly t;
    constructor(t: ServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    changePassword(req: ChangePasswordRequest): ChangePasswordResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getMyInfo(req: EmptyRequest): GetMyInfoResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    listSettablePolicies(req: EmptyRequest): ListSettablePoliciesResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    setMyClaim(req: SetMyClaimRequest): SetMyClaimResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    removeMyClaim(req: RemoveMyClaimRequest): RemoveMyClaimResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    setMyClaimSharing(req: SetMyClaimSharingRequest): SetMyClaimSharingResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    createProfile(req: CreateProfileRequest): CreateProfileResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    requestVerification(req: RequestVerificationRequest): RequestVerificationResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    listVerifiedContactMethods(req: EmptyRequest): ListVerifiedContactMethodsResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    revokeVerifiedContactMethod(req: RevokeVerifiedContactMethodRequest): RevokeVerifiedContactMethodResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    requestContactVerification(req: RequestContactVerificationRequest): RequestContactVerificationResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    confirmContactVerification(req: ConfirmContactVerificationRequest): ConfirmContactVerificationResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    enrollApplicationInstance(req: EnrollApplicationInstanceRequest): EnrollApplicationInstanceResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    listActAsGrants(req: EmptyRequest): ListActAsGrantsResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    revokeActAsGrant(req: RevokeActAsGrantRequest): RevokeActAsGrantResponse;
}
export declare class ActAsClient {
    private readonly t;
    constructor(t: ServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    refreshGrant(req: RefreshActAsGrantRequest): RefreshActAsGrantResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getGrantRevocations(req: GetActAsGrantRevocationsRequest): GetActAsGrantRevocationsResponse;
}
export declare class AdminClient {
    private readonly t;
    constructor(t: ServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    listUsers(req: ListUsersRequest): ListUsersResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getUser(req: GetUserRequest): GetUserResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getUserAuthenticationActivity(req: GetUserAuthenticationActivityRequest): GetUserAuthenticationActivityResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    createUser(req: CreateUserRequest): CreateUserResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    updateUser(req: UpdateUserRequest): UpdateUserResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    deactivateUser(req: DeactivateUserRequest): DeactivateUserResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    activateUser(req: ActivateUserRequest): ActivateUserResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    purgeUser(req: PurgeUserRequest): PurgeUserResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    revokeDomainKey(req: RevokeDomainKeyRequest): RevokeDomainKeyResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    resetPassword(req: ResetPasswordRequest): ResetPasswordResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    authenticate(req: AuthenticateRequest): AuthenticateResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    removeCredential(req: RemoveCredentialRequest): RemoveCredentialResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    setClaim(req: SetClaimRequest): SetClaimResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    removeClaim(req: RemoveClaimRequest): RemoveClaimResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    listUserClaims(req: ListUserClaimsRequest): ListUserClaimsResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getUserClaims(req: AdminUserClaimsRequest): AdminUserClaimsResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    setUserClaim(req: SetUserClaimRequest): SetUserClaimResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    listSettablePolicies(req: EmptyRequest): ListSettablePoliciesResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    listClaimTypes(req: EmptyRequest): ListClaimTypesResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    setClaimType(req: SetClaimTypeRequest): SetClaimTypeResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    removeClaimType(req: RemoveClaimTypeRequest): RemoveClaimTypeResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    setClaimTypeLabel(req: SetClaimTypeLabelRequest): SetClaimTypeLabelResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    removeClaimTypeLabel(req: RemoveClaimTypeLabelRequest): RemoveClaimTypeLabelResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    listTrustedIssuers(req: EmptyRequest): ListTrustedIssuersResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    addTrustedIssuer(req: AddTrustedIssuerRequest): AddTrustedIssuerResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    removeTrustedIssuer(req: RemoveTrustedIssuerRequest): RemoveTrustedIssuerResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    listReleaseRules(req: EmptyRequest): ListReleaseRulesResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    setReleaseRule(req: SetReleaseRuleRequest): SetReleaseRuleResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    removeReleaseRule(req: RemoveReleaseRuleRequest): RemoveReleaseRuleResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    listPendingClaimApprovals(req: EmptyRequest): ListPendingClaimApprovalsResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    approveClaim(req: ApproveClaimRequest): ApproveClaimResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    rejectClaim(req: RejectClaimRequest): RejectClaimResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    adminIssueAttestation(req: AdminIssueAttestationRequest): AdminIssueAttestationResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    grantRelation(req: GrantRelationRequest): GrantRelationResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    removeRelation(req: RemoveRelationRequest): RemoveRelationResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    listRelations(req: ListRelationsRequest): ListRelationsResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    checkPermission(req: CheckPermissionRequest): CheckPermissionResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    recheckPins(req: RecheckPinsRequest): RecheckPinsResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    listLocalRps(req: ListLocalRpsRequest): ListLocalRpsResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getLocalRp(req: GetLocalRpRequest): GetLocalRpResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    approveLocalRp(req: ApproveLocalRpRequest): ApproveLocalRpResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    denyLocalRp(req: DenyLocalRpRequest): DenyLocalRpResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    revokeLocalRp(req: RevokeLocalRpRequest): RevokeLocalRpResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getLocalRpPolicy(req: GetLocalRpPolicyRequest): GetLocalRpPolicyResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    setLocalRpPolicy(req: SetLocalRpPolicyRequest): SetLocalRpPolicyResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    purgeLocalRpTickets(req: PurgeLocalRpTicketsRequest): PurgeLocalRpTicketsResponse;
}
export declare class ApplicationKeysClient {
    private readonly t;
    constructor(t: ServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getApplicationKeys(req: GetApplicationKeysRequest): GetApplicationKeysResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    startKeyChallenge(req: StartApplicationKeyChallengeRequest): StartApplicationKeyChallengeResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    addKey(req: AddApplicationKeyRequest): AddApplicationKeyResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    renewAttestation(req: RenewApplicationKeyAttestationRequest): RenewApplicationKeyAttestationResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    revokeKey(req: RevokeApplicationKeyRequest): RevokeApplicationKeyResponse;
}
export declare class AttestationClient {
    private readonly t;
    constructor(t: ServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    depositClaim(req: DepositClaimRequest): DepositClaimResponse;
}
export declare class BrowserAuthorizationClient {
    private readonly t;
    constructor(t: ServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    inspect(req: BrowserAuthorizationInspectRequest): BrowserAuthorizationInspectResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    complete(req: BrowserAuthorizationCompleteRequest): BrowserAuthorizationCompleteResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    inspectActAs(req: BrowserActAsInspectRequest): BrowserActAsInspectResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    completeActAs(req: BrowserActAsCompleteRequest): BrowserActAsCompleteResponse;
}
export declare class DomainKeysClient {
    private readonly t;
    constructor(t: ServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getDomainKeys(req: EmptyRequest): GetDomainKeysResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getRevocations(req: GetRevocationsRequest): GetRevocationsResponse;
}
export declare class GuestbookClient {
    private readonly t;
    constructor(t: ServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    createEntry(req: CreateGuestbookRequest): GuestbookEntry;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    listEntries(req: GuestbookListRequest): GuestbookListResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    updateEntry(req: UpdateGuestbookRequest): GuestbookEntry;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    deleteEntry(req: DeleteGuestbookRequest): DeleteGuestbookResponse;
}
export declare class HandshakeClient {
    private readonly t;
    constructor(t: ServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    handshake(req: HandshakeRequest): HandshakeResponse;
}
export declare class HelloClient {
    private readonly t;
    constructor(t: ServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    hello(req: HelloRequest): HelloResponse;
}
export declare class I18nClient {
    private readonly t;
    constructor(t: ServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getTranslations(req: TranslationsRequest): TranslationsResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    listLocales(req: EmptyRequest): ListLocalesResponse;
}
export declare class IdentityClient {
    private readonly t;
    constructor(t: ServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getUserInfo(req: SignedUserInfoRequest): UserInfo;
}
export declare class LocalRpClient {
    private readonly t;
    constructor(t: ServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    redeemClaimTicket(req: SignedLocalRpTicketRedemptionRequest): LocalRpTicketRedemptionResponse;
}
export declare class NotificationClient {
    private readonly t;
    constructor(t: ServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getCapabilities(req: EmptyRequest): GetNotificationCapabilitiesResponse;
}
export declare class OpsClient {
    private readonly t;
    constructor(t: ServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    healthcheck(req: EmptyRequest): CheckResult;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    readiness(req: EmptyRequest): CheckResult;
}
export declare class RecoveryClient {
    private readonly t;
    constructor(t: ServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    requestPasswordRecovery(req: RequestPasswordRecoveryRequest): RequestPasswordRecoveryResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    validatePasswordRecovery(req: ValidatePasswordRecoveryRequest): ValidatePasswordRecoveryResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    completePasswordRecovery(req: CompletePasswordRecoveryRequest): CompletePasswordRecoveryResponse;
}
export declare class RpClient {
    private readonly t;
    constructor(t: ServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    signRequest(req: RpSignRequest): RpSignResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    decryptToken(req: RpDecryptRequest): RpDecryptResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    verifyAssertion(req: RpVerifyRequest): RpVerifyResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    userinfoFetch(req: RpUserInfoRequest): UserInfo;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    issueAttestation(req: RpIssueAttestationRequest): RpIssueAttestationResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    resolveDomainKeys(req: RpResolveDomainKeysRequest): RpResolveDomainKeysResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    resolveApplicationKeys(req: RpResolveApplicationKeysRequest): RpResolveApplicationKeysResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    actAsRefreshGrant(req: RpActAsRefreshRequest): RefreshActAsGrantResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    resolveActAsRevocations(req: RpResolveActAsRevocationsRequest): GetActAsGrantRevocationsResponse;
}
export declare class SessionClient {
    private readonly t;
    constructor(t: ServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    loginPassword(req: SessionPasswordLoginRequest): SessionPasswordLoginResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getCurrent(req: EmptyRequest): SessionCurrentResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    logout(req: EmptyRequest): SessionLogoutResponse;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    introspect(req: IntrospectBrowserSessionRequest): IntrospectBrowserSessionResponse;
}
export declare class UiClient {
    private readonly t;
    constructor(t: ServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getConfiguration(req: EmptyRequest): GetUiConfigurationResponse;
}
export declare class UserKeysClient {
    private readonly t;
    constructor(t: ServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getUserKeys(req: GetUserKeysRequest): GetUserKeysResponse;
}
export declare class ApiClient {
    readonly account: AccountClient;
    readonly actAs: ActAsClient;
    readonly admin: AdminClient;
    readonly applicationKeys: ApplicationKeysClient;
    readonly attestation: AttestationClient;
    readonly browserAuthorization: BrowserAuthorizationClient;
    readonly domainKeys: DomainKeysClient;
    readonly guestbook: GuestbookClient;
    readonly handshake: HandshakeClient;
    readonly hello: HelloClient;
    readonly i18n: I18nClient;
    readonly identity: IdentityClient;
    readonly localRp: LocalRpClient;
    readonly notification: NotificationClient;
    readonly ops: OpsClient;
    readonly recovery: RecoveryClient;
    readonly rp: RpClient;
    readonly session: SessionClient;
    readonly ui: UiClient;
    readonly userKeys: UserKeysClient;
    constructor(t: ServiceTransport);
}
