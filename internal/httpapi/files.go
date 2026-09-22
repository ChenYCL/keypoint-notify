package httpapi

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/light/keypoint-notify/internal/model"
	"github.com/light/keypoint-notify/internal/store"
)

// humanBytes renders a size the way an error message wants it.
func humanBytes(n int64) string {
	if n >= 1<<20 {
		return fmt.Sprintf("%d MB", n>>20)
	}
	return fmt.Sprintf("%d KB", n>>10)
}

// uploadCeiling is the hard read cap, one megabyte above the real limit so a
// slightly-oversized file reaches SaveFile and gets the precise error rather
// than being cut off mid-stream by the reader.
const uploadCeiling = store.MaxBlobBytes + (1 << 20)

// ---------------------------------------------------------------------------
// POST /api/v1/files                 standalone upload, reference the id later
// POST /api/v1/tasks/{code}/files    upload straight onto a task
// ---------------------------------------------------------------------------

// handleFileUpload accepts a standalone upload; the caller keeps the returned
// ids and references them later from a report or a segment body.
func (s *Server) handleFileUpload(w http.ResponseWriter, r *http.Request) {
	s.upload(w, r, "")
}

// handleFileUploadToTask binds the upload to a task immediately, so it shows up
// in that task's attachment list without a second call.
func (s *Server) handleFileUploadToTask(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	t, err := s.St.GetTask(code)
	if err != nil {
		respondError(w, s.taskNotFound(code, err))
		return
	}
	s.upload(w, r, t.ID)
}

// upload accepts multipart/form-data with one or more `file` parts and returns
// attachment descriptors rather than a redirect, because the caller is usually
// a CLI or an agent that wants the id to embed in a report or a segment body.
func (s *Server) upload(w http.ResponseWriter, r *http.Request, taskID string) {
	r.Body = http.MaxBytesReader(w, r.Body, uploadCeiling)
	if err := r.ParseMultipartForm(uploadCeiling); err != nil {
		// A body that blew the reader cap is a size problem, not a syntax
		// problem: saying "malformed multipart" here sends the caller off to
		// fix a request that was fine.
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			respondError(w, NewError(http.StatusRequestEntityTooLarge, "file_too_large",
				"附件超过 %s 上限", humanBytes(store.MaxBlobBytes)).
				WithHint("更大的内容改用链接：任务的 links 字段，或分段正文里的 URL"))
			return
		}
		respondError(w, NewError(http.StatusBadRequest, "bad_multipart",
			"解析上传失败: %v", err).
			WithHint(`用 multipart/form-data，字段名 file。示例：curl -H "Authorization: Bearer $KP_KEY" -F file=@shot.png http://<host>/api/v1/files`))
		return
	}
	files := r.MultipartForm.File["file"]
	if len(files) == 0 {
		// Accept any field name as a fallback: a caller that guessed
		// `attachment` should still succeed rather than get a puzzling 400.
		for name, fhs := range r.MultipartForm.File {
			Verbosef("upload used field %q instead of \"file\"", name)
			files = fhs
			break
		}
	}
	if len(files) == 0 {
		respondError(w, NewError(http.StatusBadRequest, "no_file", "没有收到文件").
			WithHint("multipart 字段名用 file；一次可以传多个同名字段"))
		return
	}

	actor := identity(r)
	scope, refID := "none", ""
	sideID := ""
	if taskID != "" {
		scope, refID = "task", taskID
		if sideKey := strings.TrimSpace(r.FormValue("side_key")); sideKey != "" {
			if sd, err := s.St.Side(taskID, sideKey); err == nil {
				sideID = sd.ID
			}
		}
	}

	attachments := []model.Attachment{}
	for _, fh := range files {
		f, err := fh.Open()
		if err != nil {
			respondError(w, err)
			return
		}
		mime := fh.Header.Get("Content-Type")
		if mime == "" || mime == "application/octet-stream" {
			mime = sniffMIME(f)
		}
		att, err := s.St.SaveFile(fh.Filename, mime, actor.Name, taskID, sideID, scope, refID, f)
		f.Close()
		if err != nil {
			respondError(w, err)
			return
		}
		attachments = append(attachments, att)
	}

	resp := map[string]any{
		"ok": true, "count": len(attachments), "files": attachments,
		"markdown": markdownFor(attachments),
		"hint":     "把 markdown 片段贴进分段正文，或在上报时带 attachments: [\"<id>\"]",
	}
	if taskID != "" {
		_ = s.St.AddWatcher(taskID, actor.ID)
		if t, err := s.St.GetTask(taskID); err == nil {
			if ev, err := s.St.Emit(store.EmitInput{
				Type: store.EvFileUploaded, Actor: actor, Task: &t, SideID: sideID,
				Kind: "file", Title: t.Code + " 新增附件 " + attachments[0].Name,
				Payload: map[string]any{"count": len(attachments), "files": attachmentIDs(attachments)},
			}); err == nil {
				resp["event_id"] = ev.ID
			}
		}
	}
	writeJSON(w, http.StatusCreated, resp)
}

func attachmentIDs(atts []model.Attachment) []string {
	out := make([]string, 0, len(atts))
	for _, a := range atts {
		out = append(out, a.ID)
	}
	return out
}

func markdownFor(atts []model.Attachment) []string {
	out := make([]string, 0, len(atts))
	for _, a := range atts {
		if a.IsImage {
			out = append(out, fmt.Sprintf("![%s](%s)", a.Name, a.URL))
		} else {
			out = append(out, fmt.Sprintf("[%s](%s)", a.Name, a.URL))
		}
	}
	return out
}

// sniffMIME classifies an upload that arrived without a usable Content-Type.
// Without this a screenshot posted by a naive client lands as
// application/octet-stream and loses its inline preview.
func sniffMIME(f io.ReadSeeker) string {
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "application/octet-stream"
	}
	return http.DetectContentType(buf[:n])
}

// ---------------------------------------------------------------------------
// GET /api/v1/files/{id}
// ---------------------------------------------------------------------------

func (s *Server) handleFileGet(w http.ResponseWriter, r *http.Request) {
	att, f, err := s.St.OpenFile(r.PathValue("id"))
	if err != nil {
		respondError(w, NewError(http.StatusNotFound, "file_not_found",
			"附件 %s 不存在", r.PathValue("id")).
			WithHint("附件 id 形如 fil_xxx；上传返回的 files[].id 就是它"))
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		respondError(w, err)
		return
	}

	// Images render inline; everything else downloads. SVG is excluded from
	// inline rendering because an inlined SVG can execute script in the page.
	disposition := "attachment"
	if att.IsImage {
		disposition = "inline"
	}
	w.Header().Set("Content-Type", att.MIME)
	w.Header().Set("Content-Disposition",
		disposition+`; filename="`+sanitizeFilename(att.Name)+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("ETag", `"`+att.SHA256+`"`)
	http.ServeContent(w, r, att.Name, info.ModTime(), f)
}

func sanitizeFilename(name string) string {
	name = strings.NewReplacer(`"`, "", "\n", "", "\r", "", "\\", "_", "/", "_").Replace(name)
	if strings.TrimSpace(name) == "" {
		return "file"
	}
	return name
}
