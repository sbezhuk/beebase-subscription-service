package apple

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

var (
	ErrMalformedJWS          = errors.New("malformed jws token")
	ErrUnsupportedAlgorithm  = errors.New("unsupported signing algorithm")
	ErrMissingCertificates   = errors.New("missing certificate chain in jws header")
	ErrInvalidCertificate    = errors.New("invalid x5c certificate in jws header")
	ErrCertificateVerifyFail = errors.New("apple root certificate verification failed")
	ErrInvalidSignature      = errors.New("jws signature verification failed")
	ErrInvalidPublicKey      = errors.New("leaf certificate public key is not ecdsa")
	ErrInvalidLeafOID        = errors.New("leaf certificate missing apple server notification oid")
)

// Apple Server Notification extension OID (1.2.840.113635.100.6.11.1).
var appleServerNotificationOID = asn1.ObjectIdentifier{1, 2, 840, 113635, 100, 6, 11, 1}

// decodeBase64URL decodes a base64url-encoded string, tolerating optional trailing '=' padding.
func decodeBase64URL(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

// Verifier defines the interface for verifying and decoding Apple JWS tokens.
type Verifier interface {
	VerifyNotification(signedPayload string) (*NotificationPayload, error)
	VerifyTransaction(signedTransactionInfo string) (*TransactionInfo, error)
	VerifyRenewalInfo(signedRenewalInfo string) (*RenewalInfo, error)
}

// DefaultVerifier verifies Apple JWS tokens against an x509 root certificate pool.
type DefaultVerifier struct {
	rootPool *x509.CertPool
}

// NewDefaultVerifier returns a DefaultVerifier backed by rootPool.
// If rootPool is nil, AppleRootCertPool() is used by default.
func NewDefaultVerifier(rootPool *x509.CertPool) *DefaultVerifier {
	if rootPool == nil {
		rootPool = AppleRootCertPool()
	}
	return &DefaultVerifier{rootPool: rootPool}
}

type jwsHeader struct {
	Alg string   `json:"alg"`
	X5c []string `json:"x5c"`
}

// verifyJWS decodes, validates the certificate chain against rootPool, verifies the ES256
// signature, and unmarshals the payload into dest.
func (v *DefaultVerifier) verifyJWS(tokenString string, dest any) error {
	parts := strings.Split(tokenString, ".")
	if len(parts) != 3 {
		return ErrMalformedJWS
	}

	headerBytes, err := decodeBase64URL(parts[0])
	if err != nil {
		return fmt.Errorf("%w: header decode: %v", ErrMalformedJWS, err)
	}

	var hdr jwsHeader
	if err := json.Unmarshal(headerBytes, &hdr); err != nil {
		return fmt.Errorf("%w: header json: %v", ErrMalformedJWS, err)
	}

	if hdr.Alg != "ES256" {
		return fmt.Errorf("%w: expected ES256, got %s", ErrUnsupportedAlgorithm, hdr.Alg)
	}

	if len(hdr.X5c) == 0 {
		return ErrMissingCertificates
	}

	certs := make([]*x509.Certificate, len(hdr.X5c))
	for i, certB64 := range hdr.X5c {
		der, err := base64.StdEncoding.DecodeString(certB64)
		if err != nil {
			return fmt.Errorf("%w: cert %d base64 decode: %v", ErrInvalidCertificate, i, err)
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			return fmt.Errorf("%w: cert %d parse: %v", ErrInvalidCertificate, i, err)
		}
		certs[i] = cert
	}

	leaf := certs[0]
	intermediates := x509.NewCertPool()
	for i := 1; i < len(certs); i++ {
		intermediates.AddCert(certs[i])
	}

	verifyOpts := x509.VerifyOptions{
		Roots:         v.rootPool,
		Intermediates: intermediates,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}

	if _, err := leaf.Verify(verifyOpts); err != nil {
		return fmt.Errorf("%w: %v", ErrCertificateVerifyFail, err)
	}

	// SEC-01: Validate that leaf certificate contains Apple Server Notification OID (1.2.840.113635.100.6.11.1).
	hasAppleOID := false
	for _, ext := range leaf.Extensions {
		if ext.Id.Equal(appleServerNotificationOID) {
			hasAppleOID = true
			break
		}
	}
	if !hasAppleOID {
		return ErrInvalidLeafOID
	}

	pubKey, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return ErrInvalidPublicKey
	}

	// Normalize token parts by trimming base64url padding for RFC 7515 compatibility.
	rawToken := strings.TrimRight(parts[0], "=") + "." +
		strings.TrimRight(parts[1], "=") + "." +
		strings.TrimRight(parts[2], "=")

	parser := jwt.NewParser(jwt.WithValidMethods([]string{"ES256"}))
	token, err := parser.Parse(rawToken, func(t *jwt.Token) (any, error) {
		return pubKey, nil
	})
	if err != nil || !token.Valid {
		return fmt.Errorf("%w: %v", ErrInvalidSignature, err)
	}

	payloadBytes, err := decodeBase64URL(parts[1])
	if err != nil {
		return fmt.Errorf("%w: payload decode: %v", ErrMalformedJWS, err)
	}

	if err := json.Unmarshal(payloadBytes, dest); err != nil {
		return fmt.Errorf("%w: payload json unmarshal: %v", ErrMalformedJWS, err)
	}

	return nil
}

// VerifyNotification verifies and parses an Apple App Store Server Notifications V2 payload.
func (v *DefaultVerifier) VerifyNotification(signedPayload string) (*NotificationPayload, error) {
	var payload NotificationPayload
	if err := v.verifyJWS(signedPayload, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

// VerifyTransaction verifies and parses an Apple signedTransactionInfo payload.
func (v *DefaultVerifier) VerifyTransaction(signedTransactionInfo string) (*TransactionInfo, error) {
	var tx TransactionInfo
	if err := v.verifyJWS(signedTransactionInfo, &tx); err != nil {
		return nil, err
	}
	return &tx, nil
}

// VerifyRenewalInfo verifies and parses an Apple signedRenewalInfo payload.
func (v *DefaultVerifier) VerifyRenewalInfo(signedRenewalInfo string) (*RenewalInfo, error) {
	if signedRenewalInfo == "" {
		return nil, nil
	}
	var renewal RenewalInfo
	if err := v.verifyJWS(signedRenewalInfo, &renewal); err != nil {
		return nil, err
	}
	return &renewal, nil
}
