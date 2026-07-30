package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"fmt"
)

var encoding = base32.StdEncoding.WithPadding(base32.NoPadding)

type JobIdentity struct {
	JobID     string
	NonceHash string
}

func New() (JobIdentity, error) {
	jobBytes := make([]byte, 10)
	nonce := make([]byte, 16)
	if _, err := rand.Read(jobBytes); err != nil {
		return JobIdentity{}, fmt.Errorf("generate job id: %w", err)
	}
	if _, err := rand.Read(nonce); err != nil {
		return JobIdentity{}, fmt.Errorf("generate job nonce: %w", err)
	}
	digest := sha256.Sum256(nonce)
	return JobIdentity{
		JobID:     "dt_" + encoding.EncodeToString(jobBytes),
		NonceHash: "sha256:" + hex.EncodeToString(digest[:]),
	}, nil
}

func SHA256(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
