package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/light/keypoint-notify/internal/model"
)

// MaxBlobBytes caps a single upload. Attachments are meant to be screenshots,
// logs and diagrams, not build artifacts; anything bigger belongs in a link.
const MaxBlobBytes = 32 << 20 // 32 MiB

// ErrTooLarge is returned when an upload exceeds MaxBlobBytes.
var ErrTooLarge = errors.New("file too large")

// SaveFile streams r into blob storage and records it. Identical content is
// stored once: blobs are addressed by content hash, so re-uploading the same
// screenshot costs nothing.
func (s *Store) SaveFile(name, mime, uploader, taskID, sideID, scope, refID string, r io.Reader) (model.Attachment, error) {
	tmp, err := os.CreateTemp(filepath.Join(s.root, "blobs"), "upload-*")
	if err != nil {
		return model.Attachment{}, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once renamed into place

	hasher := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(r, MaxBlobBytes+1))
	if cerr := tmp.Close(); cerr != nil && err == nil {
		err = cerr
	}
	if err != nil {
		return model.Attachment{}, err
	}
	if size > MaxBlobBytes {
		return model.Attachment{}, fmt.Errorf("%w: %d bytes exceeds the %d byte limit", ErrTooLarge, size, MaxBlobBytes)
	}

	sum := hex.EncodeToString(hasher.Sum(nil))
	rel := filepath.Join("blobs", sum[:2], sum)
	abs := filepath.Join(s.root, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return model.Attachment{}, err
	}
	if _, statErr := os.Stat(abs); statErr != nil {
		if err := os.Rename(tmpPath, abs); err != nil {
			return model.Attachment{}, fmt.Errorf("persist blob: %w", err)
		}
	}

	if name = strings.TrimSpace(name); name == "" {
		name = "file"
	}
	if mime = strings.TrimSpace(mime); mime == "" {
		mime = "application/octet-stream"
	}
	now := nowMS()
	att := model.Attachment{
		ID:      NewID("fil_"),
		Name:    name,
		MIME:    mime,
		Size:    size,
		SHA256:  sum,
		IsImage: IsImageMIME(mime),
	}
	att.URL = "/api/v1/files/" + att.ID

	_, err = s.db.Exec(
		`INSERT INTO files(id, sha256, name, mime, size, rel_path, uploader, task_id, side_id, scope, ref_id, created_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		att.ID, sum, att.Name, att.MIME, size, rel, uploader, taskID, sideID, scope, refID, now)
	if err != nil {
		return model.Attachment{}, err
	}
	return att, nil
}

// File loads one attachment's metadata.
func (s *Store) File(id string) (model.Attachment, string, error) {
	var (
		att model.Attachment
		rel string
	)
	err := s.db.QueryRow(
		`SELECT id, name, mime, size, sha256, rel_path FROM files WHERE id = ?`, id).
		Scan(&att.ID, &att.Name, &att.MIME, &att.Size, &att.SHA256, &rel)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Attachment{}, "", fmt.Errorf("%w: file %s", ErrNotFound, id)
	}
	if err != nil {
		return model.Attachment{}, "", err
	}
	att.URL = "/api/v1/files/" + att.ID
	att.IsImage = IsImageMIME(att.MIME)
	return att, filepath.Join(s.root, rel), nil
}

// OpenFile returns a reader over the stored bytes.
func (s *Store) OpenFile(id string) (model.Attachment, *os.File, error) {
	att, abs, err := s.File(id)
	if err != nil {
		return model.Attachment{}, nil, err
	}
	f, err := os.Open(abs)
	if err != nil {
		return model.Attachment{}, nil, fmt.Errorf("blob missing on disk: %w", err)
	}
	return att, f, nil
}

// AttachFile moves a previously uploaded file onto a task or side.
func (s *Store) AttachFile(fileID, taskID, sideID, scope, refID string) error {
	_, err := s.db.Exec(
		`UPDATE files SET task_id=?, side_id=?, scope=?, ref_id=? WHERE id=?`,
		taskID, sideID, scope, refID, fileID)
	return err
}

// TaskFiles returns generic attachments for a task (scope "task"), excluding
// files that were bound to a specific report.
func (s *Store) TaskFiles(taskID string) ([]model.Attachment, error) {
	return s.FilesByScope("task", taskID)
}

// FilesByScope lists attachments bound to one scope/ref pair.
func (s *Store) FilesByScope(scope, refID string) ([]model.Attachment, error) {
	if refID == "" {
		return []model.Attachment{}, nil
	}
	rows, err := s.db.Query(
		`SELECT id, name, mime, size, sha256 FROM files
		 WHERE scope = ? AND ref_id = ? ORDER BY created_at`, scope, refID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Attachment{}
	for rows.Next() {
		var att model.Attachment
		if err := rows.Scan(&att.ID, &att.Name, &att.MIME, &att.Size, &att.SHA256); err != nil {
			return nil, err
		}
		att.URL = "/api/v1/files/" + att.ID
		att.IsImage = IsImageMIME(att.MIME)
		out = append(out, att)
	}
	return out, rows.Err()
}

// IsImageMIME reports whether a MIME type is previewable inline.
func IsImageMIME(mime string) bool {
	return strings.HasPrefix(mime, "image/") &&
		mime != "image/svg+xml" // svg can carry script; never inline it
}
