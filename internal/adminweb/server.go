// Package adminweb serves the administrator UI only on a random loopback port.
package adminweb

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/r3t2/ezgamelan/internal/admin"
	"github.com/r3t2/ezgamelan/internal/store"
)

const (
	idleTimeout       = 10 * time.Minute
	actionTimeout     = 40 * time.Second
	maxLocalBody      = 8 << 10
	maxLocalResponse  = admin.MaxResponseSize + 4096
	concurrentActions = 1
)

//go:embed assets/*
var assets embed.FS

type Server struct {
	remote       Remote
	serverTarget string
	listener     net.Listener
	http         *http.Server
	basePath     string
	origin       string
	url          string
	assets       fs.FS
	slots        chan struct{}
	lastRequest  atomic.Int64
	done         chan error
	closed       chan struct{}
	closeOnce    sync.Once
}

type localActionRequest struct {
	Request admin.Request `json:"request"`
}

type localConfig struct {
	Server string `json:"server"`
}

func Start(ctx context.Context, remote Remote, serverTarget string) (*Server, error) {
	if remote == nil {
		return nil, errors.New("SSH 调用器为空")
	}
	if err := ValidateServerAlias(serverTarget); err != nil {
		return nil, fmt.Errorf("SSH 目标: %w", err)
	}
	session, err := randomSession()
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("启动本机管理界面: %w", err)
	}
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		listener.Close()
		return nil, fmt.Errorf("加载管理界面资源: %w", err)
	}
	address := listener.Addr().String()
	basePath := "/" + session + "/"
	server := &Server{
		remote:       remote,
		serverTarget: serverTarget,
		listener:     listener,
		basePath:     basePath,
		origin:       "http://" + address,
		url:          "http://" + address + basePath,
		assets:       sub,
		slots:        make(chan struct{}, concurrentActions),
		done:         make(chan error, 1),
		closed:       make(chan struct{}),
	}
	server.lastRequest.Store(time.Now().UnixNano())
	server.http = &http.Server{
		Handler:           server,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      45 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    8 << 10,
	}
	go server.run(ctx)
	go server.watchIdle(ctx)
	return server, nil
}

func (s *Server) URL() string {
	return s.url
}

func (s *Server) Wait() error {
	return <-s.done
}

func (s *Server) Close(ctx context.Context) error {
	var result error
	s.closeOnce.Do(func() {
		result = s.http.Shutdown(ctx)
	})
	return result
}

func (s *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	s.setHeaders(writer)
	if request.Host != s.listener.Addr().String() {
		http.Error(writer, "invalid host", http.StatusBadRequest)
		return
	}
	if !strings.HasPrefix(request.URL.Path, s.basePath) {
		http.NotFound(writer, request)
		return
	}
	s.lastRequest.Store(time.Now().UnixNano())
	relative := strings.TrimPrefix(request.URL.Path, s.basePath)
	switch {
	case request.Method == http.MethodGet && relative == "":
		s.serveAsset(writer, request, "index.html", "text/html; charset=utf-8")
	case request.Method == http.MethodGet && relative == "app.js":
		s.serveAsset(writer, request, "app.js", "text/javascript; charset=utf-8")
	case request.Method == http.MethodGet && relative == "style.css":
		s.serveAsset(writer, request, "style.css", "text/css; charset=utf-8")
	case request.Method == http.MethodGet && relative == "api/config":
		s.writeJSON(writer, http.StatusOK, localConfig{Server: s.serverTarget})
	case request.Method == http.MethodPost && relative == "api/action":
		s.handleAction(writer, request)
	case request.Method == http.MethodPost && relative == "api/close":
		if !s.validOrigin(request) {
			http.Error(writer, "invalid origin", http.StatusForbidden)
			return
		}
		s.writeJSON(writer, http.StatusOK, map[string]bool{"ok": true})
		go func() {
			timer := time.NewTimer(100 * time.Millisecond)
			defer timer.Stop()
			<-timer.C
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = s.Close(ctx)
		}()
	default:
		http.NotFound(writer, request)
	}
}

func (s *Server) handleAction(writer http.ResponseWriter, request *http.Request) {
	if !s.validOrigin(request) {
		http.Error(writer, "invalid origin", http.StatusForbidden)
		return
	}
	if contentType := request.Header.Get("Content-Type"); !strings.HasPrefix(
		strings.ToLower(contentType),
		"application/json",
	) {
		http.Error(writer, "invalid content type", http.StatusUnsupportedMediaType)
		return
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		s.writeJSON(writer, http.StatusTooManyRequests, admin.Response{
			ProtocolVersion: admin.ProtocolVersion,
			OK:              false,
			Error: &admin.ErrorDetail{
				Code:    "E_INTERNAL_BUSY",
				Message: "上一项管理操作仍在进行，请稍后重试",
			},
		})
		return
	}

	body := http.MaxBytesReader(writer, request.Body, maxLocalBody)
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var input localActionRequest
	if err := decoder.Decode(&input); err != nil {
		http.Error(writer, "invalid request", http.StatusBadRequest)
		return
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		http.Error(writer, "invalid request", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), actionTimeout)
	defer cancel()
	response, err := s.remote.Call(ctx, s.serverTarget, input.Request)
	if err != nil {
		response = admin.Response{
			ProtocolVersion: admin.ProtocolVersion,
			OK:              false,
			Error: &admin.ErrorDetail{
				Code:    "E_SERVICE_SSH",
				Message: err.Error(),
			},
		}
	}
	response = webResponse(response)
	s.writeJSON(writer, http.StatusOK, response)
}

func webResponse(response admin.Response) admin.Response {
	if response.Snapshot == nil {
		return response
	}
	snapshot := *response.Snapshot
	snapshot.Devices = make([]admin.Device, 0, len(response.Snapshot.Devices))
	for _, device := range response.Snapshot.Devices {
		if device.Status != store.StatusRevoked {
			snapshot.Devices = append(snapshot.Devices, device)
		}
	}
	response.Snapshot = &snapshot
	return response
}

func (s *Server) validOrigin(request *http.Request) bool {
	return request.Header.Get("Origin") == s.origin
}

func (s *Server) serveAsset(
	writer http.ResponseWriter,
	request *http.Request,
	name, contentType string,
) {
	data, err := fs.ReadFile(s.assets, name)
	if err != nil {
		http.Error(writer, "missing asset", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", contentType)
	writer.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(data)
}

func (s *Server) writeJSON(writer http.ResponseWriter, status int, value any) {
	var buffer strings.Builder
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(value); err != nil || buffer.Len() > maxLocalResponse {
		http.Error(writer, "response error", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_, _ = io.WriteString(writer, buffer.String())
}

func (s *Server) setHeaders(writer http.ResponseWriter) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set(
		"Content-Security-Policy",
		"default-src 'none'; script-src 'self'; style-src 'self'; "+
			"connect-src 'self'; img-src 'self' data:; base-uri 'none'; "+
			"form-action 'none'; frame-ancestors 'none'",
	)
	writer.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	writer.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("X-Frame-Options", "DENY")
}

func (s *Server) run(ctx context.Context) {
	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = s.Close(shutdownCtx)
		case <-s.closed:
		}
	}()
	err := s.http.Serve(s.listener)
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	close(s.closed)
	s.done <- err
}

func (s *Server) watchIdle(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.closed:
			return
		case now := <-ticker.C:
			last := time.Unix(0, s.lastRequest.Load())
			if now.Sub(last) < idleTimeout {
				continue
			}
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = s.Close(shutdownCtx)
			cancel()
			return
		}
	}
}

func randomSession() (string, error) {
	data := make([]byte, 24)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("生成本机会话: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}
