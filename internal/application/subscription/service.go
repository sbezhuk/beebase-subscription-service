// Package subscription implements the application use cases for subscription management
// and store webhook processing.
package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/sbezhuk/beebase-subscription-service/internal/domain/subscription"
	"github.com/sbezhuk/beebase-subscription-service/internal/platform/apple"
	"github.com/sbezhuk/beebase-subscription-service/internal/platform/google"
)

var (
	ErrInvalidWebhookPayload = errors.New("invalid webhook payload")
	ErrUnsupportedProduct    = errors.New("unsupported subscription product")
	ErrBundleIDMismatch      = errors.New("bundle id mismatch")
)

// Supported BeeBase Pro subscription products.
const (
	ProductProMonthly = "beebase_pro_monthly"
	ProductProYearly  = "beebase_pro_yearly"
)

// googleSubscriptionID is the Google Play subscription product ID for BeeBase Pro.
const googleSubscriptionID = "beebase_pro"

// Service coordinates subscription domain operations, repository persistence, and store notifications.
type Service struct {
	repo        subscription.Repository
	verifier    apple.Verifier
	bundleID    string
	expectedEnv subscription.Environment
	log         *slog.Logger

	googleClient      google.Client
	googlePackageName string
	appleAPI          apple.SubscriptionAPI
}

// NewService constructs a Service with the provided repository, verifier, and configuration.
func NewService(repo subscription.Repository, verifier apple.Verifier, bundleID string, expectedEnv subscription.Environment, log *slog.Logger) *Service {
	return &Service{
		repo:        repo,
		verifier:    verifier,
		bundleID:    bundleID,
		expectedEnv: expectedEnv,
		log:         log,
	}
}

// WithGoogle sets the Google Play API client and default Android package name.
func (s *Service) WithGoogle(client google.Client, packageName string) *Service {
	s.googleClient = client
	s.googlePackageName = packageName
	return s
}

// WithAppleAPI enables authoritative App Store Server API reconciliation after
// local StoreKit JWS verification. A nil client preserves local-only behavior
// for isolated unit tests and development configurations.
func (s *Service) WithAppleAPI(client apple.SubscriptionAPI) *Service {
	s.appleAPI = client
	return s
}

// DeleteAllByUser removes only BeeBase's local subscription projection. The
// App Store/Play purchase remains provider-owned; future webhooks are ignored
// because no local account mapping exists after this operation.
func (s *Service) DeleteAllByUser(ctx context.Context, userID uuid.UUID) error {
	r, ok := s.repo.(interface {
		DeleteAllByUser(context.Context, uuid.UUID) error
	})
	if !ok {
		return fmt.Errorf("subscription repository does not support account cleanup")
	}
	return r.DeleteAllByUser(ctx, userID)
}

// HandleAppleNotification processes an incoming App Store Server Notifications V2 payload.
func (s *Service) HandleAppleNotification(ctx context.Context, signedPayload string) error {
	if signedPayload == "" {
		return fmt.Errorf("%w: empty signedPayload", ErrInvalidWebhookPayload)
	}

	// 1. Cryptographically verify and decode outer notification JWS
	notification, err := s.verifier.VerifyNotification(signedPayload)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidWebhookPayload, err)
	}

	if notification.NotificationUUID == "" {
		return fmt.Errorf("%w: missing notificationUUID", ErrInvalidWebhookPayload)
	}

	// 2. Cryptographically verify and decode signed transaction information
	if notification.Data.SignedTransactionInfo == "" {
		s.log.Info("apple notification missing signedTransactionInfo, skipping transaction update",
			"notificationUUID", notification.NotificationUUID,
			"notificationType", notification.NotificationType,
		)
		// Still record event for idempotency
		rawPayload, _ := json.Marshal(notification)
		event := subscription.NewEvent(subscription.ProviderApple, notification.NotificationUUID, notification.NotificationType, rawPayload)
		_, _ = s.repo.RecordEventIfNotExists(ctx, event)
		return nil
	}

	txInfo, err := s.verifier.VerifyTransaction(notification.Data.SignedTransactionInfo)
	if err != nil {
		return fmt.Errorf("%w: verify transaction: %v", ErrInvalidWebhookPayload, err)
	}

	// 3. Cryptographically verify and decode signed renewal info if present (VER-01: fail closed on error)
	var renewalInfo *apple.RenewalInfo
	if notification.Data.SignedRenewalInfo != "" {
		renewal, err := s.verifier.VerifyRenewalInfo(notification.Data.SignedRenewalInfo)
		if err != nil {
			return fmt.Errorf("%w: verify renewal info: %v", ErrInvalidWebhookPayload, err)
		}
		renewalInfo = renewal
	}

	// 4. Product, bundle, and environment validation (ENV-01: fail closed on mismatch)
	if txInfo.BundleID != s.bundleID {
		s.log.Warn("ignoring notification for mismatched bundle id",
			"notificationUUID", notification.NotificationUUID,
			"got_bundle_id", txInfo.BundleID,
			"expected_bundle_id", s.bundleID,
		)
		return nil
	}

	incomingEnv := mapEnvironment(txInfo.Environment)
	if incomingEnv != s.expectedEnv {
		s.log.Warn("ignoring notification for mismatched environment (ENV-01)",
			"notificationUUID", notification.NotificationUUID,
			"incoming_env", incomingEnv,
			"expected_env", s.expectedEnv,
		)
		return nil
	}

	if !isSupportedProduct(txInfo.ProductID) {
		s.log.Warn("ignoring notification for unsupported product id",
			"notificationUUID", notification.NotificationUUID,
			"product_id", txInfo.ProductID,
		)
		return nil
	}

	// 5. Map Apple notification type + subtype + renewal info to domain Status
	targetStatus, autoRenew, shouldProcess := mapAppleStatus(notification.NotificationType, notification.Subtype, renewalInfo)
	if !shouldProcess {
		s.log.Info("unhandled or informational apple notification type, skipping subscription update",
			"notificationUUID", notification.NotificationUUID,
			"notificationType", notification.NotificationType,
			"subtype", notification.Subtype,
		)
		// Record event for idempotency & audit
		rawPayload, _ := json.Marshal(notification)
		event := subscription.NewEvent(subscription.ProviderApple, notification.NotificationUUID, notification.NotificationType, rawPayload)
		_, _ = s.repo.RecordEventIfNotExists(ctx, event)
		return nil
	}

	// 6. Execute idempotency and subscription state update within a single database transaction
	return s.repo.WithTx(ctx, func(txRepo subscription.Repository) error {
		rawPayload, _ := json.Marshal(notification)
		event := subscription.NewEvent(subscription.ProviderApple, notification.NotificationUUID, notification.NotificationType, rawPayload)

		inserted, err := txRepo.RecordEventIfNotExists(ctx, event)
		if err != nil {
			return fmt.Errorf("record event idempotency: %w", err)
		}
		if !inserted {
			s.log.Info("apple notification already processed, skipping mutation",
				"notificationUUID", notification.NotificationUUID,
			)
			return nil
		}

		// 7. Resolve user: appAccountToken -> existing subscription by originalTransactionId
		var (
			userID      uuid.UUID
			existingSub *subscription.Subscription
		)

		if txInfo.AppAccountToken != "" {
			if parsed, err := uuid.Parse(txInfo.AppAccountToken); err == nil && parsed != uuid.Nil {
				userID = parsed
			}
		}

		// Look up existing subscription by provider + original_transaction_id
		existing, err := txRepo.FindByProviderAndTransaction(ctx, subscription.ProviderApple, txInfo.OriginalTransactionID)
		if err == nil {
			existingSub = existing
			if userID == uuid.Nil {
				userID = existing.UserID
			}
		} else if !errors.Is(err, subscription.ErrNotFound) {
			return fmt.Errorf("lookup existing subscription: %w", err)
		}

		// If user cannot be resolved, do not invent a fake user.
		if userID == uuid.Nil {
			s.log.Info("unresolved apple subscription event (no user mapping available yet)",
				"notificationUUID", notification.NotificationUUID,
				"notificationType", notification.NotificationType,
				"originalTransactionId", txInfo.OriginalTransactionID,
				"productId", txInfo.ProductID,
			)
			return nil
		}

		var incomingExpiresAt *time.Time
		if txInfo.ExpiresDate > 0 {
			t := time.UnixMilli(txInfo.ExpiresDate).UTC()
			incomingExpiresAt = &t
		}

		var incomingEventAt *time.Time
		if notification.SignedDate > 0 {
			t := time.UnixMilli(notification.SignedDate).UTC()
			incomingEventAt = &t
		} else if txInfo.SignedDate > 0 {
			t := time.UnixMilli(txInfo.SignedDate).UTC()
			incomingEventAt = &t
		}

		// 8. Out-of-order check (ORD-01: compare signedDate / incomingEventAt)
		if existingSub != nil && !shouldApplyUpdate(existingSub, targetStatus, incomingExpiresAt, incomingEventAt) {
			s.log.Info("skipping out-of-order or invalid apple state update",
				"notificationUUID", notification.NotificationUUID,
				"currentStatus", existingSub.Status,
				"targetStatus", targetStatus,
			)
			return nil
		}

		now := time.Now().UTC()
		env := incomingEnv

		if existingSub != nil {
			existingSub.ProductID = txInfo.ProductID
			existingSub.OriginalTransactionID = &txInfo.OriginalTransactionID
			existingSub.TransactionID = &txInfo.TransactionID
			existingSub.Status = targetStatus
			existingSub.Environment = &env
			existingSub.ExpiresAt = incomingExpiresAt
			existingSub.AutoRenew = &autoRenew
			if incomingEventAt != nil {
				existingSub.LastEventAt = incomingEventAt
			}

			if targetStatus == subscription.StatusCancelled {
				if existingSub.CancelledAt == nil {
					existingSub.CancelledAt = &now
				}
			} else if targetStatus == subscription.StatusActive {
				existingSub.CancelledAt = nil
			}

			existingSub.UpdatedAt = now

			if err := txRepo.Update(ctx, existingSub); err != nil {
				return fmt.Errorf("update subscription: %w", err)
			}

			s.log.Info("updated apple subscription",
				"subscription_id", existingSub.ID,
				"user_id", existingSub.UserID,
				"status", existingSub.Status,
				"notification_type", notification.NotificationType,
			)
		} else {
			newSub, err := subscription.New(userID, subscription.ProviderApple, txInfo.ProductID)
			if err != nil {
				return fmt.Errorf("construct subscription: %w", err)
			}
			newSub.OriginalTransactionID = &txInfo.OriginalTransactionID
			newSub.TransactionID = &txInfo.TransactionID
			newSub.Status = targetStatus
			newSub.Environment = &env
			newSub.ExpiresAt = incomingExpiresAt
			newSub.AutoRenew = &autoRenew
			if incomingEventAt != nil {
				newSub.LastEventAt = incomingEventAt
			}

			if targetStatus == subscription.StatusCancelled {
				newSub.CancelledAt = &now
			}

			newSub.UpdatedAt = now

			if err := txRepo.Create(ctx, newSub); err != nil {
				return fmt.Errorf("create subscription: %w", err)
			}

			s.log.Info("created apple subscription from webhook",
				"subscription_id", newSub.ID,
				"user_id", newSub.UserID,
				"status", newSub.Status,
				"notification_type", notification.NotificationType,
			)
		}

		return nil
	})
}

func isSupportedProduct(productID string) bool {
	return productID == ProductProMonthly || productID == ProductProYearly
}

func mapEnvironment(raw string) subscription.Environment {
	lower := strings.ToLower(raw)
	if lower == "production" {
		return subscription.EnvironmentProduction
	}
	return subscription.EnvironmentSandbox
}

// mapAppleStatus translates Apple NotificationType + Subtype + RenewalInfo into domain Status.
func mapAppleStatus(notificationType, subtype string, renewal *apple.RenewalInfo) (status subscription.Status, autoRenew bool, ok bool) {
	switch notificationType {
	case apple.NotificationTypeSubscribed:
		return subscription.StatusActive, true, true

	case apple.NotificationTypeDidRenew:
		return subscription.StatusActive, true, true

	case apple.NotificationTypeDidChangeRenewalStatus:
		if subtype == apple.SubtypeAutoRenewDisabled || (renewal != nil && renewal.AutoRenewStatus == 0) {
			return subscription.StatusCancelled, false, true
		}
		if subtype == apple.SubtypeAutoRenewEnabled || (renewal != nil && renewal.AutoRenewStatus == 1) {
			return subscription.StatusActive, true, true
		}
		// SEM-01: Fail closed on unknown renewal status subtype without mutating state.
		return "", false, false

	case apple.NotificationTypeDidFailToRenew:
		// If Apple indicates grace period, grant grace_period status
		if subtype == apple.SubtypeGracePeriod || (renewal != nil && renewal.GracePeriodExpiresDate > time.Now().UnixMilli()) {
			return subscription.StatusGracePeriod, false, true
		}
		return subscription.StatusBillingRetry, false, true

	case apple.NotificationTypeGracePeriodExpired:
		return subscription.StatusExpired, false, true

	case apple.NotificationTypeExpired:
		return subscription.StatusExpired, false, true

	case apple.NotificationTypeRefund, apple.NotificationTypeRevoke:
		return subscription.StatusRevoked, false, true

	default:
		// Unknown or informational types (CONSUMPTION_REQUEST, TEST, etc.)
		return "", false, false
	}
}

// shouldApplyUpdate verifies that an incoming event is not strictly older than the current state
// and that the state transition is permissible.
func shouldApplyUpdate(existing *subscription.Subscription, incomingStatus subscription.Status, incomingExpiresAt *time.Time, incomingEventAt *time.Time) bool {
	if existing == nil {
		return true
	}

	// Revoked is a terminal state for this transaction
	if existing.Status == subscription.StatusRevoked {
		return false
	}

	// ORD-01: Out-of-order event timestamp check.
	// If existing subscription has a LastEventAt timestamp, reject events with a strictly older signedDate.
	if existing.LastEventAt != nil && incomingEventAt != nil {
		if incomingEventAt.Before(*existing.LastEventAt) {
			return false
		}
	}

	// If existing subscription has an expiration date, and the incoming event has an older
	// expiration date, reject the downgrade (older out-of-order event).
	// Exception: StatusRevoked legitimately terminates access immediately and can have an earlier expiry date.
	if incomingStatus != subscription.StatusRevoked && existing.ExpiresAt != nil && incomingExpiresAt != nil {
		if incomingExpiresAt.Before(*existing.ExpiresAt) {
			return false
		}
	}

	// Validate transition rules
	if !subscription.CanTransition(existing.Status, incomingStatus) {
		return false
	}

	return true
}

// HandleGoogleNotification processes an incoming Google Cloud Pub/Sub push message containing an RTDN event.
func (s *Service) HandleGoogleNotification(ctx context.Context, payload []byte) error {
	if len(payload) == 0 {
		return fmt.Errorf("%w: empty payload", ErrInvalidWebhookPayload)
	}

	// 1. Parse and decode the Pub/Sub push envelope and inner RTDN payload
	env, notif, err := google.ParsePubSubNotification(payload)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidWebhookPayload, err)
	}

	// 2. Handle Google Play Console test notifications
	if notif.TestNotification != nil {
		s.log.Info("received google play test notification",
			"messageId", env.Message.MessageID,
			"packageName", notif.PackageName,
		)
		event := subscription.NewEvent(subscription.ProviderGoogle, env.Message.MessageID, "TEST_NOTIFICATION", payload)
		_, _ = s.repo.RecordEventIfNotExists(ctx, event)
		return nil
	}

	// 3. Ensure this is a subscription notification
	subNotif := notif.SubscriptionNotification
	if subNotif == nil {
		s.log.Info("ignoring non-subscription google play notification",
			"messageId", env.Message.MessageID,
			"packageName", notif.PackageName,
		)
		event := subscription.NewEvent(subscription.ProviderGoogle, env.Message.MessageID, "NON_SUBSCRIPTION", payload)
		_, _ = s.repo.RecordEventIfNotExists(ctx, event)
		return nil
	}

	// 4. Validate package name and subscription product ID
	expectedPackage := s.googlePackageName
	if expectedPackage == "" {
		expectedPackage = "com.beebase.production"
	}
	if notif.PackageName != expectedPackage {
		s.log.Warn("ignoring google notification for mismatched package name",
			"messageId", env.Message.MessageID,
			"got_package_name", notif.PackageName,
			"expected_package_name", expectedPackage,
		)
		return nil
	}

	const expectedSubscriptionID = googleSubscriptionID
	if subNotif.SubscriptionID != expectedSubscriptionID {
		s.log.Warn("ignoring google notification for unsupported subscription product",
			"messageId", env.Message.MessageID,
			"got_subscription_id", subNotif.SubscriptionID,
			"expected_subscription_id", expectedSubscriptionID,
		)
		return nil
	}

	if subNotif.PurchaseToken == "" {
		return fmt.Errorf("%w: missing purchase token", ErrInvalidWebhookPayload)
	}

	// 5. Use Google Play Developer API as the authoritative source of subscription state
	if s.googleClient == nil {
		return errors.New("google client is not configured")
	}

	googleSub, err := s.googleClient.GetSubscription(ctx, notif.PackageName, subNotif.SubscriptionID, subNotif.PurchaseToken)
	if err != nil {
		if errors.Is(err, google.ErrSubscriptionNotFound) {
			s.log.Warn("google subscription not found upstream",
				"messageId", env.Message.MessageID,
				"subscriptionId", subNotif.SubscriptionID,
				"purchaseToken", maskToken(subNotif.PurchaseToken),
			)
			// Record event to prevent infinite duplicate retries
			event := subscription.NewEvent(subscription.ProviderGoogle, env.Message.MessageID, fmt.Sprintf("TYPE_%d", subNotif.NotificationType), payload)
			_, _ = s.repo.RecordEventIfNotExists(ctx, event)
			return nil
		}
		return fmt.Errorf("google api lookup: %w", err)
	}

	// 6. Map Google state to domain Status
	targetStatus, autoRenew := mapGoogleStatus(subNotif.NotificationType, googleSub)

	var incomingExpiresAt *time.Time
	if !googleSub.ExpiryTime.IsZero() {
		t := googleSub.ExpiryTime.UTC()
		incomingExpiresAt = &t
	}

	var incomingEventAt *time.Time
	if notif.EventTimeMillis > 0 {
		t := notif.EventTimeMillis.Time()
		incomingEventAt = &t
	} else if env.Message.PublishTime != "" {
		if pt, err := time.Parse(time.RFC3339Nano, env.Message.PublishTime); err == nil {
			utc := pt.UTC()
			incomingEventAt = &utc
		} else if pt, err := time.Parse(time.RFC3339, env.Message.PublishTime); err == nil {
			utc := pt.UTC()
			incomingEventAt = &utc
		}
	}

	// 7. Atomic transaction: idempotency check + subscription mutation
	return s.repo.WithTx(ctx, func(txRepo subscription.Repository) error {
		eventType := fmt.Sprintf("NOTIFICATION_TYPE_%d", subNotif.NotificationType)
		event := subscription.NewEvent(subscription.ProviderGoogle, env.Message.MessageID, eventType, payload)

		inserted, err := txRepo.RecordEventIfNotExists(ctx, event)
		if err != nil {
			return fmt.Errorf("record event idempotency: %w", err)
		}
		if !inserted {
			s.log.Info("google notification already processed, skipping mutation",
				"messageId", env.Message.MessageID,
			)
			return nil
		}

		// Look up existing subscription by provider + purchase_token
		existingSub, err := txRepo.FindByProviderAndPurchaseToken(ctx, subscription.ProviderGoogle, subNotif.PurchaseToken)
		if errors.Is(err, subscription.ErrNotFound) {
			s.log.Info("unresolved google subscription event (no user mapping for purchase token)",
				"messageId", env.Message.MessageID,
				"notificationType", subNotif.NotificationType,
				"purchaseToken", maskToken(subNotif.PurchaseToken),
			)
			return nil
		}
		if err != nil {
			return fmt.Errorf("lookup existing subscription: %w", err)
		}

		// 8. Out-of-order check and state transition validation
		if !shouldApplyUpdate(existingSub, targetStatus, incomingExpiresAt, incomingEventAt) {
			s.log.Info("skipping out-of-order or invalid google state update",
				"messageId", env.Message.MessageID,
				"currentStatus", existingSub.Status,
				"targetStatus", targetStatus,
			)
			return nil
		}

		now := time.Now().UTC()
		env := subscription.EnvironmentProduction
		if googleSub.TestPurchase {
			env = subscription.EnvironmentSandbox
		}

		productID := googleSub.FullProductID()
		if productID != "" {
			existingSub.ProductID = productID
		}
		if googleSub.LatestOrderID != "" {
			existingSub.TransactionID = &googleSub.LatestOrderID
		}
		existingSub.Status = targetStatus
		existingSub.Environment = &env
		existingSub.ExpiresAt = incomingExpiresAt
		existingSub.AutoRenew = &autoRenew
		if incomingEventAt != nil {
			existingSub.LastEventAt = incomingEventAt
		}

		if targetStatus == subscription.StatusCancelled {
			if googleSub.CancelledAt != nil {
				existingSub.CancelledAt = googleSub.CancelledAt
			} else if existingSub.CancelledAt == nil {
				existingSub.CancelledAt = &now
			}
		} else if targetStatus == subscription.StatusActive {
			existingSub.CancelledAt = nil
		}

		existingSub.UpdatedAt = now

		if err := txRepo.Update(ctx, existingSub); err != nil {
			return fmt.Errorf("update subscription: %w", err)
		}

		s.log.Info("updated google subscription",
			"subscription_id", existingSub.ID,
			"user_id", existingSub.UserID,
			"status", existingSub.Status,
			"notification_type", subNotif.NotificationType,
		)

		return nil
	})
}

// mapGoogleStatus maps Google Play notification types and API subscription state to domain Status.
func mapGoogleStatus(notificationType int, sub *google.Subscription) (subscription.Status, bool) {
	// Revocation from notification type or developer cancellation takes precedence
	if notificationType == google.NotificationTypeSubscriptionRevoked ||
		sub.CancellationReason == google.CancellationReasonDeveloperInitiated {
		return subscription.StatusRevoked, false
	}

	switch sub.State {
	case google.SubscriptionStateActive:
		if !sub.AutoRenewing {
			return subscription.StatusCancelled, false
		}
		return subscription.StatusActive, true

	case google.SubscriptionStateInGracePeriod:
		return subscription.StatusGracePeriod, sub.AutoRenewing

	case google.SubscriptionStateOnHold:
		return subscription.StatusBillingRetry, false

	case google.SubscriptionStateCanceled:
		if !sub.ExpiryTime.IsZero() && sub.ExpiryTime.Before(time.Now().UTC()) {
			return subscription.StatusExpired, false
		}
		return subscription.StatusCancelled, false

	case google.SubscriptionStateExpired:
		return subscription.StatusExpired, false

	case google.SubscriptionStatePaused:
		return subscription.StatusBillingRetry, false

	default:
		// Fallback based on notification type if state is unspecified
		switch notificationType {
		case google.NotificationTypeSubscriptionRecovered,
			google.NotificationTypeSubscriptionRenewed,
			google.NotificationTypeSubscriptionPurchased,
			google.NotificationTypeSubscriptionRestarted:
			return subscription.StatusActive, true
		case google.NotificationTypeSubscriptionInGracePeriod:
			return subscription.StatusGracePeriod, false
		case google.NotificationTypeSubscriptionOnHold:
			return subscription.StatusBillingRetry, false
		case google.NotificationTypeSubscriptionCanceled:
			return subscription.StatusCancelled, false
		case google.NotificationTypeSubscriptionExpired:
			return subscription.StatusExpired, false
		case google.NotificationTypeSubscriptionRevoked:
			return subscription.StatusRevoked, false
		default:
			return subscription.StatusActive, sub.AutoRenewing
		}
	}
}

// maskToken masks a purchase token for safe logging without exposing credentials or full tokens.
func maskToken(token string) string {
	if len(token) <= 8 {
		return "***"
	}
	return token[:4] + "..." + token[len(token)-4:]
}

// Entitlement values for the subscription API response.
const (
	EntitlementFree = "free"
	EntitlementPro  = "pro"
)

// VerificationResult is returned by GetSubscription, VerifyApplePurchase,
// and VerifyGooglePurchase.
type VerificationResult struct {
	Subscription *subscription.Subscription
	Entitlement  string // EntitlementFree or EntitlementPro
}

// entitlementFor returns the entitlement string for sub.
// Uses HasActiveAccess as the authoritative source.
func entitlementFor(sub *subscription.Subscription) string {
	if sub != nil && sub.HasActiveAccess(time.Now().UTC()) {
		return EntitlementPro
	}
	return EntitlementFree
}

// GetSubscription returns the current subscription for userID.
// If no subscription record exists, returns a free-tier VerificationResult
// with a nil Subscription field (not an error).
func (s *Service) GetSubscription(ctx context.Context, userID uuid.UUID) (*VerificationResult, error) {
	sub, err := s.repo.FindByUserID(ctx, userID)
	if err != nil {
		if errors.Is(err, subscription.ErrNotFound) {
			return &VerificationResult{Entitlement: EntitlementFree}, nil
		}
		return nil, fmt.Errorf("get subscription: %w", err)
	}
	return &VerificationResult{
		Subscription: sub,
		Entitlement:  entitlementFor(sub),
	}, nil
}

// VerifyApplePurchase verifies a newly completed Apple in-app purchase
// for the authenticated user using the StoreKit 2 signedTransaction JWS.
// It upserts the subscription record and returns the resulting state.
//
// The authenticated userID always wins over any appAccountToken in the
// receipt, so a malicious client cannot claim another user's subscription.
func (s *Service) VerifyApplePurchase(ctx context.Context, userID uuid.UUID, signedTransaction string) (*VerificationResult, error) {
	if signedTransaction == "" {
		return nil, fmt.Errorf("%w: empty signedTransaction", ErrInvalidWebhookPayload)
	}

	txInfo, err := s.verifier.VerifyTransaction(signedTransaction)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidWebhookPayload, err)
	}

	if txInfo.BundleID != s.bundleID {
		return nil, fmt.Errorf("%w: bundle id mismatch", ErrBundleIDMismatch)
	}

	incomingEnv := mapEnvironment(txInfo.Environment)
	if incomingEnv != s.expectedEnv {
		s.log.Warn("verify apple: environment mismatch",
			"incoming_env", incomingEnv,
			"expected_env", s.expectedEnv,
			"user_id", userID,
		)
		return nil, fmt.Errorf("%w: environment mismatch", ErrInvalidWebhookPayload)
	}

	if !isSupportedProduct(txInfo.ProductID) {
		return nil, fmt.Errorf("%w: unsupported product %s", ErrUnsupportedProduct, txInfo.ProductID)
	}

	authoritativeStatus := subscription.StatusActive
	autoRenew := true
	if s.appleAPI != nil {
		reconciled, reconcileErr := s.reconcileApplePurchase(ctx, txInfo)
		if reconcileErr != nil {
			if errors.Is(reconcileErr, apple.ErrAPIUnavailable) {
				s.log.Warn("apple api unavailable; retaining locally verified purchase state", "user_id", userID)
			} else {
				return nil, fmt.Errorf("%w: apple api reconciliation failed", ErrInvalidWebhookPayload)
			}
		} else {
			txInfo = reconciled.Transaction
			authoritativeStatus = reconciled.Status
			autoRenew = reconciled.AutoRenew
		}
	}

	var result *VerificationResult
	err = s.repo.WithTx(ctx, func(txRepo subscription.Repository) error {
		existing, lookupErr := txRepo.FindByProviderAndTransaction(ctx, subscription.ProviderApple, txInfo.OriginalTransactionID)
		if lookupErr != nil && !errors.Is(lookupErr, subscription.ErrNotFound) {
			return fmt.Errorf("lookup subscription: %w", lookupErr)
		}

		now := time.Now().UTC()
		env := incomingEnv

		var incomingExpiresAt *time.Time
		if txInfo.ExpiresDate > 0 {
			t := time.UnixMilli(txInfo.ExpiresDate).UTC()
			incomingExpiresAt = &t
		}
		var sub *subscription.Subscription
		if existing != nil {
			existing.UserID = userID
			existing.ProductID = txInfo.ProductID
			existing.TransactionID = &txInfo.TransactionID
			existing.OriginalTransactionID = &txInfo.OriginalTransactionID
			existing.Status = authoritativeStatus
			existing.Environment = &env
			existing.ExpiresAt = incomingExpiresAt
			existing.AutoRenew = &autoRenew
			if authoritativeStatus == subscription.StatusActive || authoritativeStatus == subscription.StatusGracePeriod {
				existing.CancelledAt = nil
			}
			existing.UpdatedAt = now
			if err := txRepo.Upsert(ctx, existing); err != nil {
				return fmt.Errorf("upsert subscription: %w", err)
			}
			sub = existing
		} else {
			newSub, newErr := subscription.New(userID, subscription.ProviderApple, txInfo.ProductID)
			if newErr != nil {
				return fmt.Errorf("construct subscription: %w", newErr)
			}
			newSub.OriginalTransactionID = &txInfo.OriginalTransactionID
			newSub.TransactionID = &txInfo.TransactionID
			newSub.Status = authoritativeStatus
			newSub.Environment = &env
			newSub.ExpiresAt = incomingExpiresAt
			newSub.AutoRenew = &autoRenew
			newSub.UpdatedAt = now
			if err := txRepo.Create(ctx, newSub); err != nil {
				return fmt.Errorf("create subscription: %w", err)
			}
			sub = newSub
		}

		s.log.Info("apple purchase verified",
			"user_id", userID,
			"product_id", txInfo.ProductID,
			"original_transaction_id", txInfo.OriginalTransactionID,
			"status", sub.Status,
		)

		result = &VerificationResult{
			Subscription: sub,
			Entitlement:  entitlementFor(sub),
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

type reconciledApplePurchase struct {
	Transaction *apple.TransactionInfo
	Status      subscription.Status
	AutoRenew   bool
}

func (s *Service) reconcileApplePurchase(ctx context.Context, local *apple.TransactionInfo) (*reconciledApplePurchase, error) {
	response, err := s.appleAPI.GetSubscription(ctx, local.OriginalTransactionID)
	if err != nil {
		return nil, err
	}
	if response.Environment != "" && mapEnvironment(response.Environment) != s.expectedEnv {
		return nil, apple.ErrAPIEnvironment
	}
	var selected *apple.LastTransaction
	var selectedTx *apple.TransactionInfo
	for i := range response.LastTransactions {
		candidate := &response.LastTransactions[i]
		if candidate.SignedTransactionInfo == "" {
			continue
		}
		tx, verifyErr := s.verifier.VerifyTransaction(candidate.SignedTransactionInfo)
		if verifyErr != nil || tx.OriginalTransactionID != local.OriginalTransactionID || tx.BundleID != s.bundleID || mapEnvironment(tx.Environment) != s.expectedEnv {
			continue
		}
		candidateCopy := *candidate
		if selectedTx == nil || tx.SignedDate > selectedTx.SignedDate {
			selected = &candidateCopy
			selectedTx = tx
		}
	}
	if selected == nil || selectedTx == nil {
		return nil, apple.ErrAPIMalformed
	}
	statusCode := response.Status
	if selected.Status > 0 {
		statusCode = selected.Status
	}
	status, ok := mapAppleAPIStatus(statusCode)
	if !ok {
		return nil, apple.ErrAPIMalformed
	}
	autoRenew := status == subscription.StatusActive || status == subscription.StatusGracePeriod
	if selected.SignedRenewalInfo != "" {
		renewal, verifyErr := s.verifier.VerifyRenewalInfo(selected.SignedRenewalInfo)
		if verifyErr != nil {
			return nil, apple.ErrAPIMalformed
		}
		autoRenew = renewal.AutoRenewStatus == 1
	}
	return &reconciledApplePurchase{Transaction: selectedTx, Status: status, AutoRenew: autoRenew}, nil
}

func mapAppleAPIStatus(status int) (subscription.Status, bool) {
	switch status {
	case 1:
		return subscription.StatusActive, true
	case 2:
		return subscription.StatusExpired, true
	case 3:
		return subscription.StatusBillingRetry, true
	case 4:
		return subscription.StatusGracePeriod, true
	case 5:
		return subscription.StatusRevoked, true
	default:
		return "", false
	}
}

// VerifyGooglePurchase verifies a newly completed Google Play purchase for
// the authenticated user using the purchase token. It fetches authoritative
// state from the Google Play Developer API, upserts the subscription, and
// returns the resulting state and entitlement.
func (s *Service) VerifyGooglePurchase(ctx context.Context, userID uuid.UUID, purchaseToken, subscriptionID, packageName string) (*VerificationResult, error) {
	if purchaseToken == "" {
		return nil, fmt.Errorf("%w: empty purchaseToken", ErrInvalidWebhookPayload)
	}
	if subscriptionID == "" {
		subscriptionID = googleSubscriptionID
	}
	if packageName == "" {
		packageName = s.googlePackageName
	}
	if s.googleClient == nil {
		return nil, errors.New("google client is not configured")
	}

	googleSub, err := s.googleClient.GetSubscription(ctx, packageName, subscriptionID, purchaseToken)
	if err != nil {
		return nil, fmt.Errorf("%w: google api lookup failed", ErrInvalidWebhookPayload)
	}

	targetStatus, autoRenew := mapGoogleStatus(0, googleSub)

	var incomingExpiresAt *time.Time
	if !googleSub.ExpiryTime.IsZero() {
		t := googleSub.ExpiryTime.UTC()
		incomingExpiresAt = &t
	}

	var result *VerificationResult
	err = s.repo.WithTx(ctx, func(txRepo subscription.Repository) error {
		now := time.Now().UTC()
		env := subscription.EnvironmentProduction
		if googleSub.TestPurchase {
			env = subscription.EnvironmentSandbox
		}

		productID := googleSub.FullProductID()
		if productID == "" {
			productID = subscriptionID
		}

		existing, lookupErr := txRepo.FindByProviderAndPurchaseToken(ctx, subscription.ProviderGoogle, purchaseToken)
		if lookupErr != nil && !errors.Is(lookupErr, subscription.ErrNotFound) {
			return fmt.Errorf("lookup subscription: %w", lookupErr)
		}

		var sub *subscription.Subscription
		if existing != nil {
			existing.UserID = userID
			existing.ProductID = productID
			if googleSub.LatestOrderID != "" {
				existing.TransactionID = &googleSub.LatestOrderID
			}
			existing.Status = targetStatus
			existing.Environment = &env
			existing.ExpiresAt = incomingExpiresAt
			existing.AutoRenew = &autoRenew
			if targetStatus == subscription.StatusActive {
				existing.CancelledAt = nil
			}
			existing.UpdatedAt = now
			if err := txRepo.Upsert(ctx, existing); err != nil {
				return fmt.Errorf("upsert subscription: %w", err)
			}
			sub = existing
		} else {
			newSub, newErr := subscription.New(userID, subscription.ProviderGoogle, productID)
			if newErr != nil {
				return fmt.Errorf("construct subscription: %w", newErr)
			}
			newSub.PurchaseToken = &purchaseToken
			if googleSub.LatestOrderID != "" {
				newSub.TransactionID = &googleSub.LatestOrderID
			}
			newSub.Status = targetStatus
			newSub.Environment = &env
			newSub.ExpiresAt = incomingExpiresAt
			newSub.AutoRenew = &autoRenew
			newSub.UpdatedAt = now
			if err := txRepo.Create(ctx, newSub); err != nil {
				return fmt.Errorf("create subscription: %w", err)
			}
			sub = newSub
		}

		s.log.Info("google purchase verified",
			"user_id", userID,
			"product_id", productID,
			"status", sub.Status,
		)

		result = &VerificationResult{
			Subscription: sub,
			Entitlement:  entitlementFor(sub),
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
