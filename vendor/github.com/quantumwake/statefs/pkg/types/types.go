// Package types defines the core types and interfaces for statefs.
// All other packages depend on this package — it has no internal dependencies.
package types

import (
	"context"
	"io"
	"time"
)

// Tier represents where a block is stored.
type Tier string

const (
	// TierLocal is the local-disk tier (a mounted block device; PVC in k8s).
	TierLocal Tier = "LOCAL"
	// TierS3 is the object-storage tier for cold blocks.
	TierS3 Tier = "S3"
)

// BlockStatus tracks the lifecycle of a block.
type BlockStatus string

const (
	// BlockStatusActive marks a block that serves reads and counts toward
	// the namespace row range.
	BlockStatusActive BlockStatus = "ACTIVE"
	// BlockStatusTombstoned marks a block superseded by compaction or
	// deletion; it is invisible to reads and awaits physical cleanup
	// (Manifest.DeleteTombstoned).
	BlockStatusTombstoned BlockStatus = "TOMBSTONED"
)

// ColumnType represents the data type of a column.
type ColumnType string

const (
	ColumnTypeString  ColumnType = "string"
	ColumnTypeInt64   ColumnType = "int64"
	ColumnTypeFloat64 ColumnType = "float64"
	ColumnTypeBool    ColumnType = "bool"
	// ColumnTypeJSON is declared but not currently produced by schema
	// discovery or accepted by the writers (see Record for the value types).
	ColumnTypeJSON ColumnType = "json"
	// ColumnTypeVector is a fixed-length array of float32, carried as
	// []float32 in a Record and as raw little-endian bytes in the WAL and
	// in parquet, so it is never re-parsed after it enters the engine.
	ColumnTypeVector ColumnType = "vector"
)

// Record is a single row of data — a map of column names to values.
// Values can be string, int64, float64, bool, []float32 (a vector), or nil;
// other numeric widths are normalized to int64/float64, and any other
// structured value (maps, slices) is persisted as a JSON string by the
// parquet writer and WAL codec.
type Record map[string]any

// Block represents a parquet file containing a range of records for a namespace.
type Block struct {
	ID        string `json:"id"`        // UUID assigned at creation
	Namespace string `json:"namespace"` // owning namespace
	Tier      Tier   `json:"tier"`      // which storage tier holds the bytes
	Path      string `json:"path"`      // tier-relative path (local path or S3 key)

	// RowStart and RowEnd are the block's global row positions within the
	// namespace, both inclusive; RowCount == RowEnd - RowStart + 1.
	RowStart int64 `json:"row_start"`
	RowEnd   int64 `json:"row_end"`
	RowCount int   `json:"row_count"`

	Status    BlockStatus `json:"status"`
	CreatedAt time.Time   `json:"created_at"`
	// TombstonedAt is set (UTC) when Status becomes TOMBSTONED; nil while ACTIVE.
	TombstonedAt *time.Time `json:"tombstoned_at,omitempty"`
}

// ColumnInfo describes a column discovered from the data.
type ColumnInfo struct {
	Name     string     `json:"name"`
	Type     ColumnType `json:"type"`
	Nullable bool       `json:"nullable"`
}

// Schema describes the known columns for a namespace, derived from parquet metadata.
type Schema struct {
	Namespace string       `json:"namespace"`
	Columns   []ColumnInfo `json:"columns"`
}

// ColumnStats holds computed statistics for a single column in a namespace.
type ColumnStats struct {
	Name          string     `json:"name"`
	Type          ColumnType `json:"type"`
	RowCount      int64      `json:"row_count"`
	NullCount     int64      `json:"null_count"`
	DistinctCount int64      `json:"distinct_count"`

	// String-specific stats (omitted for non-string columns).
	MinLength int      `json:"min_length,omitempty"`
	MaxLength int      `json:"max_length,omitempty"`
	AvgLength float64  `json:"avg_length,omitempty"`
	TopValues []string `json:"top_values,omitempty"`

	// Numeric-specific stats (omitted for non-numeric columns).
	Min float64 `json:"min,omitempty"`
	Max float64 `json:"max,omitempty"`
	Avg float64 `json:"avg,omitempty"`
}

// NamespaceProfile contains column statistics and a data sample for a namespace.
// Computed during compaction and cached in the manifest for fast retrieval.
type NamespaceProfile struct {
	Namespace  string        `json:"namespace"`
	Columns    []ColumnStats `json:"columns"`
	SampleRows []Record      `json:"sample_rows"`
	TotalRows  int64         `json:"total_rows"`
	UpdatedAt  time.Time     `json:"updated_at"`
}

// ReadResult holds the result of a read operation.
type ReadResult struct {
	Records   []Record `json:"records"`    // the requested page of rows
	TotalRows int64    `json:"total_rows"` // namespace row count at read time
	// HasMore reports whether rows exist past this page; NextOffset is the
	// offset to pass to the next Read (equals TotalRows when exhausted).
	HasMore    bool  `json:"has_more"`
	NextOffset int64 `json:"next_offset"`
}

// AppendResult holds the result of an append operation.
type AppendResult struct {
	Namespace string `json:"namespace"`
	// RowStart and RowEnd are the global positions (inclusive) assigned to
	// the appended records; RowCount == len(records).
	RowStart int64 `json:"row_start"`
	RowEnd   int64 `json:"row_end"`
	RowCount int   `json:"row_count"`
	// BlockID is the UUID of the parquet block written by the direct
	// (non-WAL) append path. Empty on the WAL path: the rows are durable in
	// the WAL but not yet sealed into a block.
	BlockID string `json:"block_id"`
}

// SnapshotState describes the parquet files written for a single namespace
// (state) into a snapshot prefix, plus its total row count.
type SnapshotState struct {
	Namespace string `json:"namespace"`

	// ParquetKeys are the object keys (relative to the snapshot's storage tier,
	// i.e. the keys a reader/presigner uses) of every parquet file that makes up
	// this state. A state may consist of MULTIPLE parquet files (one per ACTIVE
	// block); readers must union all of them.
	ParquetKeys []string `json:"parquet_keys"`

	// RowCounts holds each file's row count, parallel to ParquetKeys. It comes
	// from the block manifest, so it is known at plan time — before any bytes
	// are copied — letting callers page into the right file without reading
	// the others.
	RowCounts []int64 `json:"row_counts,omitempty"`

	// ByteCounts holds each file's size in bytes, parallel to ParquetKeys, and
	// ByteSize is their sum. Sized at plan time (stat/HeadObject of the source
	// blocks) so the published manifest carries the state's on-disk size — the
	// viewer shows it up front and skips its own per-file size probes.
	ByteCounts []int64 `json:"byte_counts,omitempty"`
	ByteSize   int64   `json:"byte_size"`

	RowCount int64 `json:"row_count"`
}

// SnapshotResult reports the outcome of a Snapshot: where it was written and,
// per namespace, the parquet object keys and row counts that the caller (e.g.
// publish-api) records in its own manifest.
type SnapshotResult struct {
	// Location is the destination prefix the snapshot was written under,
	// relative to the snapshot storage tier (the S3 tier's own prefix is
	// applied on top of this when keys are resolved to a bucket).
	Location string `json:"location"`

	// States holds one entry per requested namespace that had ACTIVE blocks.
	States []SnapshotState `json:"states"`
}

// --- Interfaces ---

// Store is the main interface for statefs. Consumers interact with this.
type Store interface {
	// Append writes records to a namespace. Returns metadata about the written block.
	Append(ctx context.Context, namespace string, records []Record) (*AppendResult, error)

	// Read returns records from a namespace with pagination.
	Read(ctx context.Context, namespace string, offset, limit int64) (*ReadResult, error)

	// MaterializeLocal resolves a namespace's live blocks into local parquet file
	// paths for an external query engine (e.g. DuckDB), staging remote-tier blocks
	// on disk. The returned cleanup removes any staged temp files and is always
	// non-nil. Data is scoped to the single namespace (the unit of query isolation).
	MaterializeLocal(ctx context.Context, namespace string) (paths []string, cleanup func() error, err error)

	// Schema returns the discovered schema for a namespace.
	Schema(ctx context.Context, namespace string) (*Schema, error)

	// Compact triggers compaction for a namespace.
	Compact(ctx context.Context, namespace string) error

	// ListNamespaces returns all active namespaces.
	ListNamespaces(ctx context.Context) ([]string, error)

	// GetNamespaceRowCount returns the total number of rows in a namespace.
	GetNamespaceRowCount(ctx context.Context, namespace string) (int64, error)

	// Profile returns column statistics and a data sample for a namespace.
	// Returns a pre-computed profile if available, otherwise computes on-demand.
	Profile(ctx context.Context, namespace string) (*NamespaceProfile, error)

	// DeleteNamespace removes all blocks and data for a namespace from all tiers.
	DeleteNamespace(ctx context.Context, namespace string) error

	// Snapshot copies the ACTIVE blocks of each namespace into an immutable,
	// read-only set of parquet files under destPrefix in the S3 tier. It returns,
	// per namespace, the parquet object keys written and their row counts. The
	// live blocks are never mutated (copy-to-prefix). Requires the S3 tier to be
	// configured.
	Snapshot(ctx context.Context, namespaces []string, destPrefix string) (*SnapshotResult, error)

	// SnapshotPlan returns the same SnapshotResult (parquet keys + row counts +
	// location) as Snapshot but WITHOUT copying any bytes — manifest reads only.
	// Callers use it to return a snapshot location immediately, then run Snapshot
	// in the background to copy the blocks.
	SnapshotPlan(ctx context.Context, namespaces []string, destPrefix string) (*SnapshotResult, error)

	// Close releases resources held by the store.
	Close() error
}

// Manifest tracks blocks and their locations. Backed by SQLite.
type Manifest interface {
	// InsertBlock records a new block. It is the commit point that makes
	// sealed rows visible to manifest-driven readers.
	InsertBlock(ctx context.Context, block *Block) error

	// ListBlocks returns all ACTIVE blocks in a namespace ordered by RowStart.
	ListBlocks(ctx context.Context, namespace string) ([]*Block, error)

	// ListBlocksInRange returns the ACTIVE blocks whose [RowStart, RowEnd]
	// interval overlaps [rowStart, rowEnd], ordered by RowStart.
	ListBlocksInRange(ctx context.Context, namespace string, rowStart, rowEnd int64) ([]*Block, error)

	// TombstoneBlocks marks the given blocks TOMBSTONED. No-op on an empty slice.
	TombstoneBlocks(ctx context.Context, blockIDs []string) error

	// ReplaceBlocks atomically inserts newBlock and tombstones tombstoneIDs in a
	// single transaction. It is the compaction commit point: because the insert
	// and the tombstone are one transaction, a concurrent reader (ListBlocks /
	// Snapshot / MaterializeLocal) can never observe the merged block AND its
	// sources both ACTIVE — the window that would otherwise surface duplicate rows.
	ReplaceBlocks(ctx context.Context, newBlock *Block, tombstoneIDs []string) error

	// DeleteTombstoned permanently removes blocks tombstoned before the
	// cutoff and returns them so the caller can delete the underlying files.
	DeleteTombstoned(ctx context.Context, before time.Time) ([]*Block, error)

	// GetNamespaceRowCount returns the namespace's logical row count:
	// MAX(RowEnd)+1 over ACTIVE blocks, 0 if it has none.
	GetNamespaceRowCount(ctx context.Context, namespace string) (int64, error)

	// ListNamespaces returns the sorted namespaces that have at least one
	// ACTIVE block.
	ListNamespaces(ctx context.Context) ([]string, error)

	// DeleteNamespace removes all blocks and the profile for a namespace.
	// Returns the deleted blocks so the caller can clean up storage tiers.
	DeleteNamespace(ctx context.Context, namespace string) ([]*Block, error)

	// UpsertProfile stores a JSON-encoded namespace profile, replacing any existing one.
	UpsertProfile(ctx context.Context, namespace string, profileJSON string) error

	// GetProfile retrieves a stored profile. Returns empty string and nil error if none exists.
	GetProfile(ctx context.Context, namespace string) (profileJSON string, updatedAt time.Time, err error)

	// Close closes the underlying database.
	Close() error
}

// TierStorage handles reading and writing block data to a specific storage
// tier. All paths are tier-relative: the implementation applies its own root
// (base directory or S3 bucket/prefix).
type TierStorage interface {
	// Write stores data at path, creating parent structure as needed, and
	// returns the number of bytes written.
	Write(ctx context.Context, path string, data io.Reader) (int64, error)

	// Read opens the object at path for reading. The caller must Close the
	// returned reader.
	Read(ctx context.Context, path string) (io.ReadCloser, error)

	// Delete removes the object at path. Deleting a missing object is not
	// an error.
	Delete(ctx context.Context, path string) error

	// Exists reports whether an object is present at path.
	Exists(ctx context.Context, path string) (bool, error)

	// Size returns the object's size in bytes (os.Stat / S3 HeadObject), without
	// reading its contents.
	Size(ctx context.Context, path string) (int64, error)
}

// TierCopier is an optional capability for tiers that support an efficient
// (server-side) copy of an object already living in the same tier. The S3 tier
// implements this via S3 CopyObject, avoiding a download/reupload round trip.
type TierCopier interface {
	// Copy duplicates the object at srcPath to dstPath within the same tier.
	// Both paths are tier-relative (the tier applies its own prefix).
	Copy(ctx context.Context, srcPath, dstPath string) error
}

// BlockWriter writes records to a parquet block file.
type BlockWriter interface {
	// WriteBlock encodes records into a complete parquet file and returns
	// its bytes.
	WriteBlock(records []Record) ([]byte, error)
}

// BlockReader reads records from a parquet block file.
type BlockReader interface {
	// ReadBlock decodes up to limit records starting at offset (row index
	// within this block) from a parquet file's bytes.
	ReadBlock(data []byte, offset, limit int64) ([]Record, error)

	// ReadSchema returns the column layout of a parquet file's bytes.
	ReadSchema(data []byte) ([]ColumnInfo, error)
}
