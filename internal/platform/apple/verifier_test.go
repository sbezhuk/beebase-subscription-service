package apple_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/sbezhuk/beebase-subscription-service/internal/platform/apple"
)

type testPKI struct {
	caCert      *x509.Certificate
	caPool      *x509.CertPool
	leafCert    *x509.Certificate
	leafCertDER []byte
	caCertDER   []byte
	caKey       *ecdsa.PrivateKey
	leafKey     *ecdsa.PrivateKey
}

func setupTestPKI(t *testing.T) *testPKI {
	t.Helper()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}

	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"Apple Inc."},
			CommonName:   "Apple Root CA - Test",
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}

	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA cert: %v", err)
	}

	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate leaf key: %v", err)
	}

	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject: pkix.Name{
			Organization: []string{"Apple Inc."},
			CommonName:   "Apple Worldwide Developer Relations - Test",
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
		BasicConstraintsValid: true,
		ExtraExtensions: []pkix.Extension{
			{
				Id:    asn1.ObjectIdentifier{1, 2, 840, 113635, 100, 6, 11, 1},
				Value: []byte{},
			},
		},
	}

	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create leaf cert: %v", err)
	}

	leafCert, err := x509.ParseCertificate(leafDER)
	if err != nil {
		t.Fatalf("parse leaf cert: %v", err)
	}

	caPool := x509.NewCertPool()
	caPool.AddCert(caCert)

	return &testPKI{
		caCert:      caCert,
		caPool:      caPool,
		leafCert:    leafCert,
		leafCertDER: leafDER,
		caCertDER:   caDER,
		caKey:       caKey,
		leafKey:     leafKey,
	}
}

func signJWSWithCerts(t *testing.T, payload any, key *ecdsa.PrivateKey, certsDER ...[]byte) string {
	t.Helper()

	x5c := make([]string, len(certsDER))
	for i, der := range certsDER {
		x5c[i] = base64.StdEncoding.EncodeToString(der)
	}

	claims := jwt.MapClaims{}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		t.Fatalf("unmarshal into MapClaims: %v", err)
	}

	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	token.Header["x5c"] = x5c

	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	return signed
}

func TestDefaultVerifier_VerifyNotification(t *testing.T) {
	pki := setupTestPKI(t)
	verifier := apple.NewDefaultVerifier(pki.caPool)

	notification := apple.NotificationPayload{
		NotificationType: apple.NotificationTypeSubscribed,
		Subtype:          apple.SubtypeInitialBuy,
		NotificationUUID: "test-uuid-1234",
		Version:          "2.0",
		SignedDate:       time.Now().UnixMilli(),
		Data: apple.NotificationData{
			BundleID:    "com.beebase.production",
			Environment: "Sandbox",
		},
	}

	t.Run("valid notification succeeds", func(t *testing.T) {
		token := signJWSWithCerts(t, notification, pki.leafKey, pki.leafCertDER, pki.caCertDER)

		got, err := verifier.VerifyNotification(token)
		if err != nil {
			t.Fatalf("VerifyNotification: %v", err)
		}
		if got.NotificationUUID != notification.NotificationUUID {
			t.Errorf("got UUID %s, want %s", got.NotificationUUID, notification.NotificationUUID)
		}
		if got.NotificationType != apple.NotificationTypeSubscribed {
			t.Errorf("got type %s, want SUBSCRIBED", got.NotificationType)
		}
	})

	t.Run("invalid signature fails", func(t *testing.T) {
		otherKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("generate other key: %v", err)
		}
		// Signed by other key, but header specifies pki.leafCertDER
		token := signJWSWithCerts(t, notification, otherKey, pki.leafCertDER, pki.caCertDER)

		_, err = verifier.VerifyNotification(token)
		if err == nil {
			t.Fatalf("expected error for invalid signature, got nil")
		}
	})

	t.Run("untrusted root CA fails", func(t *testing.T) {
		otherCAKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		untrustedCAPool := x509.NewCertPool()
		untrustedCATemplate := &x509.Certificate{
			SerialNumber: big.NewInt(99),
			Subject:      pkix.Name{CommonName: "Untrusted Root"},
			NotBefore:    time.Now().Add(-1 * time.Hour),
			NotAfter:     time.Now().Add(24 * time.Hour),
			IsCA:         true,
		}
		untrustedCADER, _ := x509.CreateCertificate(rand.Reader, untrustedCATemplate, untrustedCATemplate, &otherCAKey.PublicKey, otherCAKey)
		untrustedCACert, _ := x509.ParseCertificate(untrustedCADER)
		untrustedCAPool.AddCert(untrustedCACert)

		untrustedVerifier := apple.NewDefaultVerifier(untrustedCAPool)
		token := signJWSWithCerts(t, notification, pki.leafKey, pki.leafCertDER, pki.caCertDER)

		_, err := untrustedVerifier.VerifyNotification(token)
		if err == nil {
			t.Fatalf("expected error for untrusted CA, got nil")
		}
	})

	t.Run("malformed token fails", func(t *testing.T) {
		_, err := verifier.VerifyNotification("not.a.valid.jwt.token")
		if err == nil {
			t.Fatalf("expected error for malformed token, got nil")
		}
	})

	t.Run("missing x5c header fails", func(t *testing.T) {
		token := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{"test": "claim"})
		signed, err := token.SignedString(pki.leafKey)
		if err != nil {
			t.Fatalf("sign token: %v", err)
		}

		_, err = verifier.VerifyNotification(signed)
		if err == nil {
			t.Fatalf("expected error for missing x5c, got nil")
		}
	})

	t.Run("unsupported algorithm fails", func(t *testing.T) {
		// Replace alg in header
		token := signJWSWithCerts(t, notification, pki.leafKey, pki.leafCertDER, pki.caCertDER)
		parts := strings.Split(token, ".")
		headerBytes, _ := base64.RawURLEncoding.DecodeString(parts[0])
		var hdr map[string]any
		_ = json.Unmarshal(headerBytes, &hdr)
		hdr["alg"] = "RS256"
		modifiedHdrBytes, _ := json.Marshal(hdr)
		parts[0] = base64.RawURLEncoding.EncodeToString(modifiedHdrBytes)
		modifiedToken := strings.Join(parts, ".")

		_, err := verifier.VerifyNotification(modifiedToken)
		if err == nil {
			t.Fatalf("expected error for unsupported algorithm, got nil")
		}
	})

	t.Run("missing apple OID fails (SEC-01)", func(t *testing.T) {
		noOIDLeafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("generate leaf key: %v", err)
		}
		noOIDLeafTemplate := &x509.Certificate{
			SerialNumber: big.NewInt(999),
			Subject: pkix.Name{
				Organization: []string{"Apple Inc."},
				CommonName:   "Apple Test - No OID",
			},
			NotBefore:             time.Now().Add(-1 * time.Hour),
			NotAfter:              time.Now().Add(24 * time.Hour),
			KeyUsage:              x509.KeyUsageDigitalSignature,
			ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
			BasicConstraintsValid: true,
		}
		noOIDLeafDER, err := x509.CreateCertificate(rand.Reader, noOIDLeafTemplate, pki.caCert, &noOIDLeafKey.PublicKey, pki.caKey)
		if err != nil {
			t.Fatalf("create leaf cert without OID: %v", err)
		}

		token := signJWSWithCerts(t, notification, noOIDLeafKey, noOIDLeafDER, pki.caCertDER)
		_, err = verifier.VerifyNotification(token)
		if err == nil {
			t.Fatalf("expected error for leaf certificate missing Apple notification OID, got nil")
		}
		if !errors.Is(err, apple.ErrInvalidLeafOID) {
			t.Errorf("expected ErrInvalidLeafOID, got %v", err)
		}
	})

	t.Run("tolerates base64url padding with = (DEC-01)", func(t *testing.T) {
		token := signJWSWithCerts(t, notification, pki.leafKey, pki.leafCertDER, pki.caCertDER)
		parts := strings.Split(token, ".")
		if len(parts) != 3 {
			t.Fatalf("expected 3 parts, got %d", len(parts))
		}

		// Add base64 padding '=' to header and payload parts
		pad := func(s string) string {
			if m := len(s) % 4; m != 0 {
				return s + strings.Repeat("=", 4-m)
			}
			return s
		}
		paddedToken := pad(parts[0]) + "." + pad(parts[1]) + "." + parts[2]

		got, err := verifier.VerifyNotification(paddedToken)
		if err != nil {
			t.Fatalf("VerifyNotification with padded base64url failed: %v", err)
		}
		if got.NotificationUUID != notification.NotificationUUID {
			t.Errorf("got UUID %s, want %s", got.NotificationUUID, notification.NotificationUUID)
		}
	})
}

func TestDefaultVerifier_VerifyTransactionAndRenewal(t *testing.T) {
	pki := setupTestPKI(t)
	verifier := apple.NewDefaultVerifier(pki.caPool)

	txInfo := apple.TransactionInfo{
		TransactionID:         "10001",
		OriginalTransactionID: "10001",
		BundleID:              "com.beebase.production",
		ProductID:             "beebase_pro_monthly",
		PurchaseDate:          time.Now().UnixMilli(),
		ExpiresDate:           time.Now().Add(30 * 24 * time.Hour).UnixMilli(),
		Environment:           "Production",
	}

	txToken := signJWSWithCerts(t, txInfo, pki.leafKey, pki.leafCertDER, pki.caCertDER)
	gotTx, err := verifier.VerifyTransaction(txToken)
	if err != nil {
		t.Fatalf("VerifyTransaction: %v", err)
	}
	if gotTx.TransactionID != "10001" || gotTx.ProductID != "beebase_pro_monthly" {
		t.Errorf("got tx info %+v, want transactionId 10001", gotTx)
	}

	renewalInfo := apple.RenewalInfo{
		OriginalTransactionID: "10001",
		ProductID:             "beebase_pro_monthly",
		AutoRenewStatus:       1,
		Environment:           "Production",
	}

	renewalToken := signJWSWithCerts(t, renewalInfo, pki.leafKey, pki.leafCertDER, pki.caCertDER)
	gotRenewal, err := verifier.VerifyRenewalInfo(renewalToken)
	if err != nil {
		t.Fatalf("VerifyRenewalInfo: %v", err)
	}
	if gotRenewal.AutoRenewStatus != 1 {
		t.Errorf("got autoRenewStatus %d, want 1", gotRenewal.AutoRenewStatus)
	}

	// Empty renewal info returns nil without error
	gotEmpty, err := verifier.VerifyRenewalInfo("")
	if err != nil || gotEmpty != nil {
		t.Errorf("expected (nil, nil) for empty renewal token, got (%v, %v)", gotEmpty, err)
	}
}
