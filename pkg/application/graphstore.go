package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// GraphStore is the canonical graph authority: snapshot reads, atomic
// mutation, reconciliation, and canonical attachment bytes.
type GraphStore interface {
	Current(context.Context) (*Snapshot, error)
	Apply(context.Context, string, MutationBatch, StagedBlobReader) (ApplyResult, error)
	Reconcile(context.Context, string, string) (ApplyResult, error)
	ReadAttachmentPage(context.Context, string, string, int64, int) (AttachmentPage, error)
}

// PublicationKey identifies a write within the session's durable mutation intent.
type PublicationKey struct {
	Session SessionID
	// Sequence is the session-wide sequence of the mutation_intent event.
	Sequence      uint64
	Discriminator string
}

func (k PublicationKey) Validate() error {
	if k.Session == "" || k.Sequence == 0 || k.Discriminator == "" || strings.ContainsAny(string(k.Session)+k.Discriminator, "/\r\n\x00") {
		return fmt.Errorf("publication requires a session, positive intent sequence and operation discriminator")
	}
	return nil
}

// String returns v1: followed by the SHA-256 of the NUL-separated session,
// decimal event sequence and discriminator, without exposing the session handle.
func (k PublicationKey) String() string {
	encoded := string(k.Session) + "\x00" + strconv.FormatUint(k.Sequence, 10) + "\x00" + k.Discriminator
	hash := sha256.Sum256([]byte(encoded))
	return "v1:" + hex.EncodeToString(hash[:])
}

type EntryPublication struct {
	Revision string
	Document EntryDocument
}

// EntryPublicationStore serializes publication lookup and creation without a
// graph-wide revision comparison. PublishEntry accepts one entry and its staged
// attachments. Repeated keys return the original document and revision; a lookup
// failure is never treated as absence. Required storage commits precede success.
type EntryPublicationStore interface {
	LookupEntryPublication(context.Context, PublicationKey, string) (EntryPublication, bool, error)
	PublishEntry(context.Context, PublicationKey, MutationBatch, StagedBlobReader) (EntryPublication, error)
}

type MutationBatch struct {
	ID          string
	Digest      string
	Changes     []DocumentChange
	Attachments []AttachmentMaterialization
	Message     string
	Author      Author
}

// DocumentChange is storage-neutral. CanonicalBytes are rendered once by SDD;
// Document is present when the logical artifact has structured entry form.
type DocumentChange struct {
	LogicalPath    string
	Document       *EntryDocument
	CanonicalBytes []byte
	Delete         bool
}

type Author struct {
	Name  string
	Email string
}

type ApplyState string

const (
	MutationNotApplied ApplyState = "not_applied"
	MutationApplied    ApplyState = "applied"
	MutationUnknown    ApplyState = "unknown"
)

type ApplyResult struct {
	State    ApplyState
	Revision string
}

type AppliedMutation struct {
	Project  ProjectID
	BatchID  string
	Revision string
	Batch    MutationBatch
}

// MutationFinalizer is a named, idempotent post-apply effect. It cannot
// redefine or roll back the canonical MutationBatch.
type MutationFinalizer interface {
	Name() string
	Finalize(context.Context, AppliedMutation) error
}

type BlobDigest struct {
	Algorithm string
	Value     string
}

type AttachmentMaterialization struct {
	BlobID      string
	Digest      BlobDigest
	Size        int64
	SourceName  string
	LogicalPath string
}

type AttachmentPage struct {
	// LocalPath is an optional absolute path supplied by the attachment source
	// for clients sharing its filesystem. It does not extend source retention.
	LocalPath  string
	Filename   string
	Content    []byte
	Offset     int64
	NextOffset int64
	TotalSize  int64
	More       bool
	Digest     BlobDigest
}

// StagedBlobReader limits Apply to the blobs named by its prepared batch.
type StagedBlobReader interface {
	Open(context.Context, string) (io.ReadCloser, error)
}

// MutationBatchDigest returns the SDD-owned digest over a storage-neutral
// batch. The Digest field itself is excluded.
func MutationBatchDigest(batch MutationBatch) (string, error) {
	batch.Digest = ""
	encoded, err := json.Marshal(batch)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
