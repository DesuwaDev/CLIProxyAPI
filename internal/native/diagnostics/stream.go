package diagnostics

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// Only a bounded SSE line is inspected transiently. No payload text is persisted.
// Oversized lines mark coverage incomplete, rather than pretending success.
type streamScan struct {
	line       []byte
	discard    bool
	complete   bool
	failed     bool
	eventError bool
}

func (s *streamScan) feed(p []byte) {
	for len(p) > 0 {
		end := bytes.IndexByte(p, '\n')
		part := p
		if end >= 0 {
			part = p[:end]
		}
		if !s.discard {
			if len(s.line)+len(part) > 65536 {
				s.line = nil
				s.discard = true
				s.complete = false
			} else {
				s.line = append(s.line, part...)
			}
		}
		if end < 0 {
			return
		}
		if !s.discard {
			s.parse()
		}
		s.line = nil
		s.discard = false
		p = p[end+1:]
	}
}
func (s *streamScan) finish() {
	if len(s.line) > 0 && !s.discard {
		s.parse()
	}
	s.line = nil
}
func (s *streamScan) parse() {
	line := bytes.TrimSpace(s.line)
	if len(line) == 0 {
		s.eventError = false
		return
	}
	if bytes.HasPrefix(line, []byte("event:")) {
		kind := strings.TrimSpace(string(line[6:]))
		s.eventError = kind == "error" || kind == "response.failed" || kind == "response.incomplete"
		if s.eventError {
			s.failed = true
		}
		return
	}
	if !bytes.HasPrefix(line, []byte("data:")) {
		return
	}
	data := bytes.TrimSpace(line[5:])
	if bytes.Equal(data, []byte("[DONE]")) {
		return
	}
	var v struct {
		Type     string          `json:"type"`
		Error    json.RawMessage `json:"error"`
		Response struct {
			Status string `json:"status"`
		} `json:"response"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		s.complete = false
		return
	}
	if len(v.Error) > 0 && string(v.Error) != "null" || v.Type == "error" || v.Type == "response.failed" || v.Type == "response.incomplete" || v.Response.Status == "failed" || v.Response.Status == "incomplete" {
		s.failed = true
	}
}

type observerWriter struct {
	gin.ResponseWriter
	scan        streamScan
	stream      bool
	writeFailed bool
}

func (w *observerWriter) observe(p []byte) {
	if strings.Contains(strings.ToLower(w.Header().Get("Content-Type")), "text/event-stream") {
		w.stream = true
		w.scan.feed(p)
	}
}
func (w *observerWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	if n > 0 {
		w.observe(p[:n])
	}
	if err != nil {
		w.writeFailed = true
	}
	return n, err
}
func (w *observerWriter) WriteString(p string) (int, error) {
	n, err := w.ResponseWriter.WriteString(p)
	if n > 0 {
		w.observe([]byte(p[:n]))
	}
	if err != nil {
		w.writeFailed = true
	}
	return n, err
}

func timeRange(c *gin.Context) (int64, int64, error) {
	to := time.Now().UnixMilli() + 1
	from := to - 86400000
	for _, v := range []struct {
		name   string
		target *int64
	}{{"from", &from}, {"to", &to}} {
		if raw := c.Query(v.name); raw != "" {
			n, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || n < 0 {
				return 0, 0, fmt.Errorf("invalid time range")
			}
			*v.target = n
		}
	}
	if to <= from || to-from > 366*86400000 {
		return 0, 0, fmt.Errorf("select at most 366 days")
	}
	return from, to, nil
}
