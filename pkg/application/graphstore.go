package application

import (
	"context"
	"crypto/sha1" //nolint:gosec // Git blob IDs are SHA-1 by definition; this is an identity, not a security digest.
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// GraphStore is the canonical graph authority: snapshot reads and canonical
// attachment bytes. Writes go through PublicationStore, keyed by the session's
// recorded mutation intent (d-tac-n47).
type GraphStore interface {
	Current(context.Context) (*Snapshot, error)
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

// DocumentPublication is what one keyed write left for a logical path: the
// revision carrying it and the document's bytes there, or its absence when the
// write removed the document or found nothing to remove. An empty Revision
// says the write changed nothing that still needs completing: the document
// was already present as asked, or already absent.
type DocumentPublication struct {
	Revision string
	Content  []byte
	Absent   bool
}

// DocumentMutation is one keyed write of a single graph document without
// attachments. Content with no ExpectedBlob creates the document if it is
// absent and otherwise leaves the existing document as it is; Content with an
// ExpectedBlob replaces the document that blob identifies; nil Content removes
// the document if it is present. Creation and removal are the WIP marker
// writes: a marker's path is unique to its run, so its existence is its whole
// state and no precondition applies (d-tac-lqh).
type DocumentMutation struct {
	LogicalPath string
	// Content is the complete document after the write; nil removes it.
	Content []byte
	// ExpectedBlob is the Git blob ID of the document a replacement replaces
	// (GitBlobID); a mismatch is an ErrorGraphConflict, never a retryable
	// condition (d-tac-wgw). Empty for a creation or a removal.
	ExpectedBlob string
	Message      string
}

// PublicationStore serializes publication lookup and creation without a
// graph-wide revision comparison (d-tac-n47). Repeated keys return the original
// publication; a lookup failure is never treated as absence. PublishEntry
// accepts one entry and its staged attachments; PublishDocument one entry-less
// document change: a WIP marker created or removed, a summary replaced. A
// creation over a present document and a removal of an absent one succeed
// with nothing published. Required storage commits precede success.
type PublicationStore interface {
	LookupEntryPublication(context.Context, PublicationKey, string) (EntryPublication, bool, error)
	PublishEntry(context.Context, PublicationKey, MutationBatch, StagedBlobReader) (EntryPublication, error)
	// ReadDocument returns the document's current bytes on the target, or Absent.
	ReadDocument(context.Context, string) (DocumentPublication, error)
	LookupDocumentPublication(context.Context, PublicationKey, string) (DocumentPublication, bool, error)
	PublishDocument(context.Context, PublicationKey, DocumentMutation) (DocumentPublication, error)
}

// GitBlobID is the Git object ID of content stored as a blob, the precondition
// currency of DocumentMutation: computable by any store, verifiable by a Git
// service against its tree without reading the file.
func GitBlobID(content []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(content))
	h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}

type MutationBatch struct {
	ID          string
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

// StagedBlobReader limits a publication to the blobs named by its batch.
type StagedBlobReader interface {
	Open(context.Context, string) (io.ReadCloser, error)
}
