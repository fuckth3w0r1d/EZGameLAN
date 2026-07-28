// Package enroll implements the single public enrollment endpoint.
package enroll

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/r3t2/ezgamelan/internal/invite"
)

const (
	maxRequestBody = 4 << 10
	maxConcurrent  = 16
)

type Claimer interface {
	Claim(token string, now time.Time) (invite.Profile, error)
}

type Server struct {
	claimer Claimer
	now     func() time.Time
	logger  *slog.Logger
	slots   chan struct{}
}

func New(claimer Claimer, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		claimer: claimer,
		now:     time.Now,
		logger:  logger,
		slots:   make(chan struct{}, maxConcurrent),
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/enroll", s.handleEnroll)
	return s.recover(s.limitConcurrency(mux))
}

type request struct {
	Token string `json:"token"`
}

func (s *Server) handleEnroll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "E_JOIN_METHOD", "请使用客户端加入工具")
		return
	}
	if contentType := r.Header.Get("Content-Type"); contentType != "" &&
		!strings.HasPrefix(strings.ToLower(contentType), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "E_JOIN_FORMAT", "加入请求格式错误")
		return
	}

	body := http.MaxBytesReader(w, r.Body, maxRequestBody)
	defer body.Close()
	var req request
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "E_JOIN_FORMAT", "加入请求格式错误")
		return
	}
	if err := ensureEOF(dec); err != nil || invite.ValidateToken(req.Token) != nil {
		writeError(w, http.StatusBadRequest, "E_JOIN_FORMAT", "加入请求格式错误")
		return
	}

	profile, err := s.claimer.Claim(req.Token, s.now())
	if err != nil {
		s.logger.Info("enrollment rejected", "code", "E_JOIN_TOKEN")
		writeError(w, http.StatusNotFound, "E_JOIN_TOKEN", "加入链接已失效，请联系管理员重新生成")
		return
	}
	data, err := invite.Marshal(profile, s.now())
	if err != nil {
		s.logger.Error("encode claimed profile", "code", "E_INTERNAL_PROFILE", "error", err)
		writeError(w, http.StatusInternalServerError, "E_INTERNAL_PROFILE", "服务器生成配置失败")
		return
	}
	w.Header().Set("Content-Type", "application/vnd.ezgamelan.profile+json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(data); err != nil {
		s.logger.Warn("write enrollment response", "code", "E_JOIN_TRANSFER", "error", err)
		return
	}
	s.logger.Info("enrollment completed", "client", profile.ClientName)
}

func (s *Server) limitConcurrency(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case s.slots <- struct{}{}:
			defer func() { <-s.slots }()
			next.ServeHTTP(w, r)
		default:
			writeError(w, http.StatusServiceUnavailable, "E_JOIN_BUSY", "加入服务繁忙，请稍后重试")
		}
	})
}

func (s *Server) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.logger.Error("enrollment handler panic", "code", "E_INTERNAL_HTTP")
				writeError(w, http.StatusInternalServerError, "E_INTERNAL_HTTP", "加入服务内部错误")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"code":    code,
		"message": message,
	})
}

func ensureEOF(dec *json.Decoder) error {
	var extra any
	err := dec.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("trailing JSON value")
	}
	return fmt.Errorf("decode trailing data: %w", err)
}

// EncodeRequest is used by the Windows client and tests to avoid putting the
// token into a URL or form-encoded log field.
func EncodeRequest(token string) ([]byte, error) {
	if err := invite.ValidateToken(token); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(request{Token: token}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
