package queue

import (
	"errors"
	"testing"
	"time"

	"github.com/snowmerak/ticketing/internal/apierr"
)

func testSigner(t *testing.T) *TicketSigner {
	t.Helper()
	signer, err := NewTicketSigner("k1", map[string][]byte{"k1": []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func TestTicketRoundTripAndBinding(t *testing.T) {
	signer := testSigner(t)
	now := time.Unix(1_700_000_000, 0)
	token, want, err := signer.New(100, "epoch", 7, "user-1", now, 20*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	got, err := signer.Verify(token, "user-1", 100, 100, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if got.TicketID != want.TicketID || got.Seq != 7 {
		t.Fatalf("claims = %#v, want ticket %s seq 7", got, want.TicketID)
	}
	if _, err := signer.Verify(token, "other", 100, 100, now); !errors.Is(err, apierr.ErrTicketInvalid) {
		t.Fatalf("wrong subject error = %v", err)
	}
	if _, err := signer.Verify(token, "user-1", 101, 100, now); !errors.Is(err, apierr.ErrTicketInvalid) {
		t.Fatalf("wrong event error = %v", err)
	}
}

func TestTicketRejectsTamperExpiryAndLargeSeq(t *testing.T) {
	signer := testSigner(t)
	now := time.Unix(1_700_000_000, 0)
	token, _, err := signer.New(100, "epoch", 101, "user-1", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signer.Verify(token+"x", "user-1", 100, 1000, now); !errors.Is(err, apierr.ErrTicketInvalid) {
		t.Fatalf("tamper error = %v", err)
	}
	if _, err := signer.Verify(token, "user-1", 100, 100, now); !errors.Is(err, apierr.ErrTicketInvalid) {
		t.Fatalf("large seq error = %v", err)
	}
	if _, err := signer.Verify(token, "user-1", 100, 1000, now.Add(time.Minute)); !errors.Is(err, apierr.ErrTicketExpired) {
		t.Fatalf("expiry error = %v", err)
	}
}

func TestTicketSerializationGolden(t *testing.T) {
	signer := testSigner(t)
	token, err := signer.Sign(TicketClaims{
		Version: 1, KeyID: "k1", EventID: 100, Epoch: "epoch", Seq: 7,
		SubjectID: "user-1", TicketID: "ticket", IssuedAtMS: 1_700_000_000_000, ExpiresAtMS: 1_700_001_200_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	const want = "eyJ2IjoxLCJraWQiOiJrMSIsImV2ZW50X2lkIjoiMTAwIiwiZXBvY2giOiJlcG9jaCIsInNlcSI6IjciLCJzdWJqZWN0X2lkIjoidXNlci0xIiwidGlja2V0X2lkIjoidGlja2V0IiwiaXNzdWVkX2F0X21zIjoxNzAwMDAwMDAwMDAwLCJleHBpcmVzX2F0X21zIjoxNzAwMDAxMjAwMDAwfQ.AfXI354z7FKbo-jEhqiY1TAcssDtw_R9SFLFHOTT3tA"
	if token != want {
		t.Fatalf("ticket serialization changed\n got: %s\nwant: %s", token, want)
	}
}
