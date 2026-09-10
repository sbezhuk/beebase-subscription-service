// Package apple provides types, certificate verification, and decoding for
// Apple App Store Server Notifications V2 and StoreKit transactions.
package apple

// NotificationType constants for Apple Server Notifications V2.
const (
	NotificationTypeSubscribed             = "SUBSCRIBED"
	NotificationTypeDidRenew               = "DID_RENEW"
	NotificationTypeDidChangeRenewalStatus = "DID_CHANGE_RENEWAL_STATUS"
	NotificationTypeDidFailToRenew         = "DID_FAIL_TO_RENEW"
	NotificationTypeExpired                = "EXPIRED"
	NotificationTypeGracePeriodExpired     = "GRACE_PERIOD_EXPIRED"
	NotificationTypeRefund                 = "REFUND"
	NotificationTypeRevoke                 = "REVOKE"
	NotificationTypeConsumptionRequest     = "CONSUMPTION_REQUEST"
	NotificationTypeOfferRedeemed          = "OFFER_REDEEMED"
	NotificationTypePriceIncrease          = "PRICE_INCREASE"
	NotificationTypeRenewalExtended        = "RENEWAL_EXTENDED"
	NotificationTypeRenewalExtension       = "RENEWAL_EXTENSION"
	NotificationTypeRefundDeclined         = "REFUND_DECLINED"
	NotificationTypeRefundReversed         = "REFUND_REVERSED"
	NotificationTypeExternalPurchaseToken  = "EXTERNAL_PURCHASE_TOKEN"
	NotificationTypeTest                   = "TEST"
)

// Subtype constants for Apple Server Notifications V2.
const (
	SubtypeInitialBuy        = "INITIAL_BUY"
	SubtypeResubscribe       = "RESUBSCRIBE"
	SubtypeAutoRenewEnabled  = "AUTO_RENEW_ENABLED"
	SubtypeAutoRenewDisabled = "AUTO_RENEW_DISABLED"
	SubtypeVoluntary         = "VOLUNTARY"
	SubtypeBillingRetry      = "BILLING_RETRY"
	SubtypePriceIncrease     = "PRICE_INCREASE"
	SubtypeGracePeriod       = "GRACE_PERIOD"
	SubtypeBillingRecovery   = "BILLING_RECOVERY"
	SubtypePending           = "PENDING"
	SubtypeSummary           = "SUMMARY"
	SubtypeFailure           = "FAILURE"
	SubtypeUnreported        = "UNREPORTED"
)

// WebhookRequestBody represents the outer JSON payload sent by Apple's notification servers.
type WebhookRequestBody struct {
	SignedPayload string `json:"signedPayload"`
}

// NotificationPayload is the decoded body of signedPayload.
type NotificationPayload struct {
	NotificationType string           `json:"notificationType"`
	Subtype          string           `json:"subtype"`
	NotificationUUID string           `json:"notificationUUID"`
	Data             NotificationData `json:"data"`
	Version          string           `json:"version"`
	SignedDate       int64            `json:"signedDate"`
}

// NotificationData contains store context, environment, and signed sub-payloads.
type NotificationData struct {
	AppAppleID            int64  `json:"appAppleId"`
	BundleID              string `json:"bundleId"`
	BundleVersion         string `json:"bundleVersion"`
	Environment           string `json:"environment"`
	SignedTransactionInfo string `json:"signedTransactionInfo"`
	SignedRenewalInfo     string `json:"signedRenewalInfo"`
	Status                int    `json:"status"`
}

// TransactionInfo is the decoded payload of signedTransactionInfo.
type TransactionInfo struct {
	TransactionID               string `json:"transactionId"`
	OriginalTransactionID       string `json:"originalTransactionId"`
	WebOrderLineItemID          string `json:"webOrderLineItemId"`
	BundleID                    string `json:"bundleId"`
	ProductID                   string `json:"productId"`
	SubscriptionGroupIdentifier string `json:"subscriptionGroupIdentifier"`
	PurchaseDate                int64  `json:"purchaseDate"`
	OriginalPurchaseDate        int64  `json:"originalPurchaseDate"`
	ExpiresDate                 int64  `json:"expiresDate"`
	Quantity                    int    `json:"quantity"`
	Type                        string `json:"type"`
	AppAccountToken             string `json:"appAccountToken"`
	InAppOwnershipType          string `json:"inAppOwnershipType"`
	SignedDate                  int64  `json:"signedDate"`
	Environment                 string `json:"environment"`
	TransactionReason           string `json:"transactionReason"`
	Storefront                  string `json:"storefront"`
	StorefrontID                string `json:"storefrontId"`
	Price                       int64  `json:"price"`
	Currency                    string `json:"currency"`
	RevocationDate              int64  `json:"revocationDate"`
	RevocationReason            int    `json:"revocationReason"`
}

// RenewalInfo is the decoded payload of signedRenewalInfo.
type RenewalInfo struct {
	OriginalTransactionID  string `json:"originalTransactionId"`
	AutoRenewProductID     string `json:"autoRenewProductId"`
	ProductID              string `json:"productId"`
	AutoRenewStatus        int    `json:"autoRenewStatus"` // 0: off, 1: on
	IsInBillingRetryPeriod bool   `json:"isInBillingRetryPeriod"`
	GracePeriodExpiresDate int64  `json:"gracePeriodExpiresDate"`
	ExpirationIntent       int    `json:"expirationIntent"`
	SignedDate             int64  `json:"signedDate"`
	Environment            string `json:"environment"`
}
