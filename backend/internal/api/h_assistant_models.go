package api

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"strataos/internal/jobs"
)

// Local models: the assistant can run on models served on the NAS itself by
// the Ollama app (App Center), whose API listens on 127.0.0.1:11434 by
// default. This file lists, downloads, imports and removes those models; the
// endpoint the assistant chats with is then http://127.0.0.1:11434/v1.

// ollamaURL is where the Ollama app API is expected (its default port).
const ollamaURL = "http://127.0.0.1:11434"

var modelNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:\-]{0,99}$`)

// ollamaCall performs one call to the local Ollama API.
func ollamaCall(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, ollamaURL+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return http.DefaultClient.Do(req)
}

type localModel struct {
	Name          string `json:"name"`
	Size          int64  `json:"size"`
	Family        string `json:"family,omitempty"`
	ParameterSize string `json:"parameterSize,omitempty"`
	Quantization  string `json:"quantization,omitempty"`
	Modified      string `json:"modified,omitempty"`
}

// ollamaTags is the answer of GET /api/tags.
type ollamaTags struct {
	Models []struct {
		Name       string    `json:"name"`
		Size       int64     `json:"size"`
		ModifiedAt time.Time `json:"modified_at"`
		Details    struct {
			Family            string `json:"family"`
			ParameterSize     string `json:"parameter_size"`
			QuantizationLevel string `json:"quantization_level"`
		} `json:"details"`
	} `json:"models"`
}

func (s *Server) assistantModelRoutes() {
	s.admin("GET /api/assistant/models", func(r *http.Request) (any, error) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		resp, err := ollamaCall(ctx, http.MethodGet, "/api/tags", nil, "")
		if err != nil {
			return map[string]any{"reachable": false, "models": []localModel{}}, nil
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return map[string]any{"reachable": false, "models": []localModel{}}, nil
		}
		var tags ollamaTags
		if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&tags); err != nil {
			return map[string]any{"reachable": false, "models": []localModel{}}, nil
		}
		out := []localModel{}
		for _, m := range tags.Models {
			out = append(out, localModel{Name: m.Name, Size: m.Size, Family: m.Details.Family,
				ParameterSize: m.Details.ParameterSize, Quantization: m.Details.QuantizationLevel, Modified: m.ModifiedAt.Format(time.RFC3339)})
		}
		return map[string]any{"reachable": true, "models": out}, nil
	})

	// Download a model from the Ollama library (several GB, streamed).
	s.admin("POST /api/assistant/models/pull", func(r *http.Request) (any, error) {
		var req struct{ Name string }
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		req.Name = strings.TrimSpace(req.Name)
		if !modelNameRe.MatchString(req.Name) {
			return nil, badRequest("invalid model name (e.g. mistral:7b)")
		}
		s.audit(r, "assistant", "Downloading model "+req.Name)
		return s.jobs.Start("assistant", "Downloading model "+req.Name, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			body, _ := json.Marshal(map[string]any{"name": req.Name, "stream": true})
			resp, err := ollamaCall(ctx, http.MethodPost, "/api/pull", bytes.NewReader(body), "application/json")
			if err != nil {
				return fmt.Errorf("Ollama is unreachable (install the Ollama app)")
			}
			defer resp.Body.Close()
			if resp.StatusCode/100 != 2 {
				msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
				return fmt.Errorf("Ollama answered %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
			}
			sc := bufio.NewScanner(io.LimitReader(resp.Body, 16<<20))
			sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
			for sc.Scan() {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				var line struct {
					Status    string `json:"status"`
					Total     int64  `json:"total"`
					Completed int64  `json:"completed"`
					Error     string `json:"error"`
				}
				if json.Unmarshal(sc.Bytes(), &line) != nil {
					continue
				}
				if line.Error != "" {
					return fmt.Errorf("%s", line.Error)
				}
				if line.Total > 0 {
					j.SetProgress(float64(line.Completed) / float64(line.Total) * 100)
				}
				if line.Status != "" && line.Total == 0 {
					j.Logf("%s", line.Status)
				}
			}
			if err := sc.Err(); err != nil {
				return err
			}
			j.Logf("Model %s ready", req.Name)
			return nil
		}), nil
	})

	// Import a GGUF file from a shared folder into Ollama.
	s.admin("POST /api/assistant/models/import", func(r *http.Request) (any, error) {
		var req struct{ Path, Name string }
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		req.Path, req.Name = filepath.Clean(req.Path), strings.TrimSpace(req.Name)
		if !strings.HasPrefix(req.Path, "/srv/strata/") || !strings.HasSuffix(strings.ToLower(req.Path), ".gguf") {
			return nil, badRequest("choose a .gguf file inside a volume")
		}
		if _, err := os.Stat(req.Path); err != nil {
			return nil, badRequest("file not found")
		}
		if req.Name == "" {
			req.Name = strings.TrimSuffix(filepath.Base(req.Path), filepath.Ext(req.Path))
		}
		if !modelNameRe.MatchString(req.Name) {
			return nil, badRequest("invalid model name")
		}
		s.audit(r, "assistant", "Importing model "+req.Name+" from "+req.Path)
		return s.jobs.Start("assistant", "Importing model "+req.Name, userOf(r), func(ctx context.Context, j *jobs.Job) error {
			f, err := os.Open(req.Path)
			if err != nil {
				return err
			}
			defer f.Close()
			j.SetProgress(1)
			j.Logf("Hashing %s", req.Path)
			h := sha256.New()
			size, err := io.Copy(h, f)
			if err != nil {
				return err
			}
			digest := "sha256:" + hex.EncodeToString(h.Sum(nil))
			if _, err := ollamaCall(ctx, http.MethodHead, "/api/blobs/"+digest, nil, ""); err != nil {
				return fmt.Errorf("Ollama is unreachable (install the Ollama app)")
			}
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				return err
			}
			j.Logf("Uploading to Ollama")
			j.SetProgress(5)
			upload, err := ollamaCall(ctx, http.MethodPost, "/api/blobs/"+digest, f, "application/octet-stream")
			if err != nil {
				return fmt.Errorf("Ollama is unreachable")
			}
			io.Copy(io.Discard, upload.Body)
			upload.Body.Close()
			if upload.StatusCode/100 != 2 {
				return fmt.Errorf("Ollama refused the file (%d)", upload.StatusCode)
			}
			j.SetProgress(80)
			create, _ := json.Marshal(map[string]any{"model": req.Name, "files": map[string]string{"model.gguf": digest}, "stream": false})
			resp, err := ollamaCall(ctx, http.MethodPost, "/api/create", bytes.NewReader(create), "application/json")
			if err != nil {
				return fmt.Errorf("Ollama is unreachable")
			}
			defer resp.Body.Close()
			var out struct {
				Error string `json:"error"`
			}
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
			_ = json.Unmarshal(msg, &out)
			if out.Error != "" {
				return fmt.Errorf("%s", out.Error)
			}
			if resp.StatusCode/100 != 2 {
				return fmt.Errorf("Ollama answered %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
			}
			j.SetProgress(100)
			j.Logf("Model %s ready (%d MB)", req.Name, size>>20)
			return nil
		}), nil
	})

	// Remove a model from Ollama.
	s.admin("POST /api/assistant/models/delete", func(r *http.Request) (any, error) {
		var req struct{ Name string }
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if !modelNameRe.MatchString(strings.TrimSpace(req.Name)) {
			return nil, badRequest("invalid model name")
		}
		body, _ := json.Marshal(map[string]string{"model": strings.TrimSpace(req.Name)})
		resp, err := ollamaCall(r.Context(), http.MethodDelete, "/api/delete", bytes.NewReader(body), "application/json")
		if err != nil {
			return nil, badRequest("Ollama is unreachable")
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return nil, badRequest(fmt.Sprintf("Ollama answered %d", resp.StatusCode))
		}
		s.audit(r, "assistant", "Deleted model "+req.Name)
		return nil, nil
	})
}
