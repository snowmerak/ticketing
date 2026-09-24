package ticket

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	mrand "math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/snowmerak/ticketing/internal/apierr"
	"github.com/snowmerak/ticketing/internal/id"
)

const Version = 2
const keyRetentionGrace = time.Minute
const maxTokenBytes = 4096
const keyAuditInterval = 5 * time.Second
const initialKeyRetry = 200 * time.Millisecond
const maxKeyRetry = 5 * time.Second

type Claims struct {
	Version     int    `json:"v"`
	KeyID       string `json:"kid"`
	EventID     uint64 `json:"event_id,string"`
	Epoch       string `json:"epoch"`
	Seq         uint64 `json:"seq,string"`
	SubjectID   string `json:"subject_id"`
	TicketID    string `json:"ticket_id"`
	IssuedAtMS  int64  `json:"issued_at_ms"`
	ExpiresAtMS int64  `json:"expires_at_ms"`
}

type signingKey struct {
	id        string
	public    ed25519.PublicKey
	private   ed25519.PrivateKey
	signUntil time.Time
}

type Signer struct {
	registry  Registry
	lifetime  time.Duration
	ticketTTL time.Duration
	mu        sync.RWMutex
	current   *signingKey
}

// NewSigner creates an unready signer. Run owns publication and rotation.
func NewSigner(registry Registry, lifetime, ticketTTL time.Duration) (*Signer, error) {
	if registry == nil || lifetime <= 0 || ticketTTL <= 0 || lifetime > 24*time.Hour || ticketTTL > time.Duration(math.MaxInt64)-lifetime-keyRetentionGrace {
		return nil, errors.New("invalid ticket signer configuration")
	}
	return &Signer{registry: registry, lifetime: lifetime, ticketTTL: ticketTTL}, nil
}

func KeyID(publicKey ed25519.PublicKey) string {
	digest := sha256.Sum256(publicKey)
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func (s *Signer) generateKey() (*signingKey, error) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	key := &signingKey{
		id: KeyID(publicKey), public: publicKey, private: privateKey,
		signUntil: time.Now().Add(s.lifetime),
	}
	return key, nil
}

// Run continuously reconciles the local signing key with the registry. Only one
// goroutine may run it per signer; requests and readiness never publish keys.
// report is called once when publication starts failing and once on recovery.
func (s *Signer) Run(ctx context.Context, attemptTimeout time.Duration, report func(error)) {
	if attemptTimeout <= 0 {
		panic("ticket signer requires a positive publication timeout")
	}
	var pending *signingKey
	retry := initialKeyRetry
	failed := false
	for ctx.Err() == nil {
		current := s.currentKey()
		now := time.Now()
		if pending != nil && !now.Before(pending.signUntil) {
			pending = nil
		}
		if pending == nil {
			if current != nil && now.Before(current.signUntil.Add(-s.rotationLead())) {
				wait := min(time.Until(current.signUntil.Add(-s.rotationLead())), keyAuditInterval)
				if !waitFor(ctx, wait) {
					return
				}
				attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
				err := s.checkPublished(attemptCtx, current)
				cancel()
				if err == nil {
					continue
				}
				pending = current // Restore a lost active public key with the same ID.
			} else {
				var err error
				pending, err = s.generateKey()
				if err != nil {
					if !failed && report != nil {
						report(err)
					}
					failed = true
					if !waitFor(ctx, jitter(retry)) {
						return
					}
					retry = min(retry*2, maxKeyRetry)
					continue
				}
			}
		}
		retention := time.Until(pending.signUntil) + s.ticketTTL + keyRetentionGrace
		attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
		err := s.registry.Publish(attemptCtx, pending.id, pending.public, retention)
		cancel()
		if err == nil && time.Now().Before(pending.signUntil) {
			if pending != current {
				s.mu.Lock()
				s.current = pending
				s.mu.Unlock()
			}
			pending = nil
			retry = initialKeyRetry
			if failed && report != nil {
				report(nil)
			}
			failed = false
			continue
		}
		if err == nil {
			pending = nil // Publication finished after this key's signing window.
			if !waitFor(ctx, jitter(initialKeyRetry)) {
				return
			}
			continue
		}
		if !failed && report != nil {
			report(registryUnavailable(err))
		}
		failed = true
		if !waitFor(ctx, jitter(retry)) {
			return
		}
		retry = min(retry*2, maxKeyRetry)
	}
}

func (s *Signer) rotationLead() time.Duration {
	return min(s.lifetime/10, time.Minute)
}

func waitFor(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(max(delay, 0))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func jitter(delay time.Duration) time.Duration {
	return delay/2 + time.Duration(mrand.Int64N(int64(delay/2)+1))
}

func (s *Signer) currentKey() *signingKey {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current
}

func (s *Signer) active() (*signingKey, error) {
	key := s.currentKey()
	if key == nil || !time.Now().Before(key.signUntil) {
		return nil, registryUnavailable(errors.New("no registered signing key is active"))
	}
	return key, nil
}

// CanSign reports only local key availability, without registry I/O. A request
// must still call New or Renew to confirm publication before signing.
func (s *Signer) CanSign() bool {
	_, err := s.active()
	return err == nil
}

// Ready checks the active verification record; issuing must not succeed on a key
// that other instances cannot look up.
func (s *Signer) Ready(ctx context.Context) error {
	key, err := s.active()
	if err != nil {
		return err
	}
	if err := s.checkPublished(ctx, key); err != nil {
		return err
	}
	if !time.Now().Before(key.signUntil) {
		return registryUnavailable(errors.New("signing key expired during publication check"))
	}
	return nil
}

func (s *Signer) checkPublished(ctx context.Context, key *signingKey) error {
	published, err := s.registry.Lookup(ctx, key.id)
	if err != nil {
		return registryUnavailable(err)
	}
	if !bytes.Equal(published, key.public) {
		return registryUnavailable(errors.New("published ticket key differs from active key"))
	}
	return nil
}

func (s *Signer) New(ctx context.Context, eventID uint64, epoch string, seq uint64, subjectID string, now time.Time, ttl time.Duration) (string, Claims, error) {
	if ttl <= 0 || ttl > s.ticketTTL {
		return "", Claims{}, errors.New("ticket TTL exceeds signing key retention")
	}
	ticketID, err := id.New()
	if err != nil {
		return "", Claims{}, err
	}
	claims := Claims{
		EventID: eventID, Epoch: epoch, Seq: seq, SubjectID: subjectID,
		TicketID: ticketID, IssuedAtMS: now.UnixMilli(), ExpiresAtMS: now.Add(ttl).UnixMilli(),
	}
	return s.sign(ctx, claims)
}

func (s *Signer) Renew(ctx context.Context, claims Claims, now time.Time, ttl time.Duration) (string, Claims, error) {
	if ttl <= 0 || ttl > s.ticketTTL {
		return "", Claims{}, errors.New("ticket TTL exceeds signing key retention")
	}
	claims.IssuedAtMS = now.UnixMilli()
	claims.ExpiresAtMS = now.Add(ttl).UnixMilli()
	return s.sign(ctx, claims)
}

func (s *Signer) sign(ctx context.Context, claims Claims) (string, Claims, error) {
	key, err := s.active()
	if err != nil {
		return "", Claims{}, err
	}
	if err := s.checkPublished(ctx, key); err != nil {
		return "", Claims{}, err
	}
	if !time.Now().Before(key.signUntil) {
		return "", Claims{}, registryUnavailable(errors.New("signing key expired during publication check"))
	}
	claims.Version = Version
	claims.KeyID = key.id
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", Claims{}, err
	}
	signature := ed25519.Sign(key.private, payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(signature), claims, nil
}

func (s *Signer) Verify(ctx context.Context, token, subjectID string, eventID, maxSeq uint64, now time.Time) (Claims, error) {
	if len(token) > maxTokenBytes {
		return Claims{}, apierr.ErrTicketInvalid
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return Claims{}, apierr.ErrTicketInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Claims{}, apierr.ErrTicketInvalid
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(signature) != ed25519.SignatureSize {
		return Claims{}, apierr.ErrTicketInvalid
	}
	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil || !validKeyID(claims.KeyID) {
		return Claims{}, apierr.ErrTicketInvalid
	}
	if claims.Version != Version {
		return Claims{}, apierr.ErrTicketInvalid
	}
	publicKey, err := s.registry.Lookup(ctx, claims.KeyID)
	if errors.Is(err, ErrKeyNotFound) {
		return Claims{}, apierr.ErrTicketInvalid
	}
	if err != nil {
		return Claims{}, registryUnavailable(err)
	}
	if len(publicKey) != ed25519.PublicKeySize || KeyID(publicKey) != claims.KeyID || !ed25519.Verify(publicKey, payload, signature) {
		return Claims{}, apierr.ErrTicketInvalid
	}
	if claims.EventID != eventID || claims.SubjectID != subjectID || claims.Seq > maxSeq || claims.Epoch == "" || claims.TicketID == "" {
		return Claims{}, apierr.ErrTicketInvalid
	}
	if now.UnixMilli() >= claims.ExpiresAtMS {
		return Claims{}, apierr.ErrTicketExpired
	}
	return claims, nil
}

func validKeyID(value string) bool {
	if len(value) != 43 {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(raw) == sha256.Size
}

func registryUnavailable(err error) error {
	return apierr.Wrap(http.StatusServiceUnavailable, "TICKET_KEY_UNAVAILABLE", "ticket verification keys are unavailable", true, fmt.Errorf("ticket key registry: %w", err))
}
