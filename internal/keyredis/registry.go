package keyredis

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/snowmerak/ticketing/internal/ticket"
)

const prefix = "ticketing:verification-key:v2:"

type Registry struct {
	client *redis.Client
}

func New(client *redis.Client) *Registry { return &Registry{client: client} }

func (r *Registry) Publish(ctx context.Context, keyID string, publicKey ed25519.PublicKey, retention time.Duration) error {
	if len(publicKey) != ed25519.PublicKeySize || ticket.KeyID(publicKey) != keyID || retention <= 0 {
		return errors.New("invalid public ticket key")
	}
	value := base64.RawURLEncoding.EncodeToString(publicKey)
	created, err := r.client.SetNX(ctx, prefix+keyID, value, retention).Result()
	if err != nil {
		return err
	}
	if created {
		return nil
	}
	existing, err := r.client.Get(ctx, prefix+keyID).Result()
	if err != nil {
		return err
	}
	if existing != value {
		return fmt.Errorf("verification key ID %s already contains different material", keyID)
	}
	return nil
}

func (r *Registry) Lookup(ctx context.Context, keyID string) (ed25519.PublicKey, error) {
	if len(keyID) != 43 {
		return nil, ticket.ErrKeyNotFound
	}
	value, err := r.client.Get(ctx, prefix+keyID).Result()
	if errors.Is(err, redis.Nil) {
		return nil, ticket.ErrKeyNotFound
	}
	if err != nil {
		return nil, err
	}
	publicKey, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(publicKey) != ed25519.PublicKeySize || ticket.KeyID(publicKey) != keyID {
		return nil, errors.New("corrupt ticket verification key")
	}
	return ed25519.PublicKey(publicKey), nil
}
