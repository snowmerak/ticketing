package queue

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/snowmerak/ticketing/internal/apierr"
	"github.com/snowmerak/ticketing/internal/id"
)

const ticketVersion = 1

type TicketClaims struct {
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

type TicketSigner struct {
	currentKeyID string
	keys         map[string][]byte
}

func NewTicketSigner(currentKeyID string, keys map[string][]byte) (*TicketSigner, error) {
	if currentKeyID == "" || len(keys[currentKeyID]) < 32 {
		return nil, errors.New("current ticket signing key must contain at least 32 bytes")
	}
	copyKeys := make(map[string][]byte, len(keys))
	for keyID, key := range keys {
		copyKeys[keyID] = append([]byte(nil), key...)
	}
	return &TicketSigner{currentKeyID: currentKeyID, keys: copyKeys}, nil
}

func (s *TicketSigner) New(eventID uint64, epoch string, seq uint64, subjectID string, now time.Time, ttl time.Duration) (string, TicketClaims, error) {
	ticketID, err := id.New()
	if err != nil {
		return "", TicketClaims{}, err
	}
	claims := TicketClaims{
		Version: ticketVersion, KeyID: s.currentKeyID, EventID: eventID, Epoch: epoch, Seq: seq,
		SubjectID: subjectID, TicketID: ticketID, IssuedAtMS: now.UnixMilli(), ExpiresAtMS: now.Add(ttl).UnixMilli(),
	}
	token, err := s.Sign(claims)
	return token, claims, err
}

func (s *TicketSigner) Renew(claims TicketClaims, now time.Time, ttl time.Duration) (string, TicketClaims, error) {
	claims.Version = ticketVersion
	claims.KeyID = s.currentKeyID
	claims.IssuedAtMS = now.UnixMilli()
	claims.ExpiresAtMS = now.Add(ttl).UnixMilli()
	token, err := s.Sign(claims)
	return token, claims, err
}

func (s *TicketSigner) Sign(claims TicketClaims) (string, error) {
	key, ok := s.keys[claims.KeyID]
	if !ok {
		return "", fmt.Errorf("unknown signing key %q", claims.KeyID)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(payload)
	signature := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (s *TicketSigner) Verify(token, subjectID string, eventID, maxSeq uint64, now time.Time) (TicketClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return TicketClaims{}, apierr.ErrTicketInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return TicketClaims{}, apierr.ErrTicketInvalid
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return TicketClaims{}, apierr.ErrTicketInvalid
	}
	var claims TicketClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return TicketClaims{}, apierr.ErrTicketInvalid
	}
	key, ok := s.keys[claims.KeyID]
	if !ok {
		return TicketClaims{}, apierr.ErrTicketInvalid
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return TicketClaims{}, apierr.ErrTicketInvalid
	}
	if claims.Version != ticketVersion || claims.EventID != eventID || claims.SubjectID != subjectID || claims.Seq > maxSeq || claims.Epoch == "" || claims.TicketID == "" {
		return TicketClaims{}, apierr.ErrTicketInvalid
	}
	if now.UnixMilli() >= claims.ExpiresAtMS {
		return TicketClaims{}, apierr.ErrTicketExpired
	}
	return claims, nil
}
