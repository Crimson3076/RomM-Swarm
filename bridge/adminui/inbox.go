package adminui

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Crimson3076/RomM-Swarm/bridge/romm"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

type inboxFile struct {
	Name      string
	SizeHuman string
}

type inboxData struct {
	baseData
	InboxConfigured bool
	InboxFiles      []inboxFile
	Platforms       []romm.Platform
}

func (s *Server) handleInboxPage(w http.ResponseWriter, r *http.Request) {
	data := inboxData{baseData: s.base(r, "Inbox"), InboxConfigured: s.InboxDir != ""}

	if data.InboxConfigured {
		entries, err := os.ReadDir(s.InboxDir)
		if err != nil {
			data.Error = "reading the inbox directory: " + err.Error()
		} else {
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				info, err := e.Info()
				if err != nil {
					continue
				}
				data.InboxFiles = append(data.InboxFiles, inboxFile{Name: e.Name(), SizeHuman: humanBytes(info.Size())})
			}
		}
	}

	if conn := s.Backend.Connection(); conn != nil && conn.Platforms != nil {
		data.Platforms = append(data.Platforms, conn.Platforms.All()...)
		sort.Slice(data.Platforms, func(i, j int) bool { return data.Platforms[i].Name < data.Platforms[j].Name })
	} else if data.Error == "" {
		data.Error = "not connected to RomM — check Settings"
	}

	renderPage(w, "inbox", data)
}

// handleImport triggers an import from either the mounted inbox directory
// or a direct browser upload, per the "source" form field.
func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	// Bound multipart staging and release parser-owned temporary files on every
	// exit path, including validation failures.
	r.Body = http.MaxBytesReader(w, r.Body, (1<<30)+(1<<20))
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		err := r.ParseMultipartForm(8 << 20)
		if r.MultipartForm != nil {
			defer r.MultipartForm.RemoveAll()
		}
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "could not read upload; maximum file size is 1 GiB")
			return
		}
	}
	platform := r.FormValue("platform")
	if platform == "" {
		writeJSONError(w, http.StatusBadRequest, "a platform is required")
		return
	}

	var localPath, displayName string
	var cleanup func()

	switch r.FormValue("source") {
	case "inbox":
		if s.InboxDir == "" {
			writeJSONError(w, http.StatusBadRequest, "no inbox directory is configured")
			return
		}
		name := r.FormValue("path")
		if name == "" || strings.Contains(name, "..") || filepath.IsAbs(name) {
			writeJSONError(w, http.StatusBadRequest, "invalid inbox file")
			return
		}
		localPath = filepath.Join(s.InboxDir, name)
		// cleanup stays nil: an inbox file belongs to the operator and is
		// never deleted by an import, successful or not.

	case "upload":
		file, header, err := r.FormFile("file")
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "no file was uploaded: "+err.Error())
			return
		}
		defer file.Close()
		if header.Size > 1<<30 {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "maximum file size is 1 GiB")
			return
		}

		tmp, err := os.CreateTemp("", "bridge-upload-*"+filepath.Ext(header.Filename))
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "could not stage the upload: "+err.Error())
			return
		}
		if _, err := io.Copy(tmp, file); err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			writeJSONError(w, http.StatusInternalServerError, "could not stage the upload: "+err.Error())
			return
		}
		tmp.Close()
		localPath = tmp.Name()
		// RomM needs the file's real name, not the randomly-generated temp
		// path it was staged under.
		displayName = filepath.Base(header.Filename)
		// This temp file is the Bridge's own, not the operator's — it is
		// safe, and necessary, to remove once the import is done with it.
		cleanup = func() { os.Remove(localPath) }

	default:
		writeJSONError(w, http.StatusBadRequest, "unknown import source")
		return
	}

	id, err := s.Backend.StartImport(localPath, platform, displayName, protocol.DefaultIngestionTimeout, cleanup)
	if err != nil {
		if cleanup != nil {
			cleanup()
		}
		writeJSONError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{
			"id": string(id), "activity_url": "/activity?id=" + string(id),
		})
		return
	}
	http.Redirect(w, r, "/activity?id="+string(id), http.StatusSeeOther)
}
