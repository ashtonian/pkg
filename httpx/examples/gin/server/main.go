// Package main demonstrates a Gin server with proper HTTP caching headers,
// compression, auth, multiple resource types, and error responses that
// exercise the full httpx transport stack.
package main

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"maps"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
)

// ---------------------------------------------------------------------------
// Domain types
// ---------------------------------------------------------------------------

// User represents a user resource.
type User struct {
	ID        int       `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Device represents an IoT device — a second resource type to demonstrate
// that httpx.Get[User] and httpx.Get[Device] work from the same client.
type Device struct {
	ID       int       `json:"id"`
	Serial   string    `json:"serial"`
	Name     string    `json:"name"`
	Status   string    `json:"status"`
	LastSeen time.Time `json:"last_seen"`
}

// APIError is the structured error body returned on 4xx/5xx.
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ---------------------------------------------------------------------------
// Stores
// ---------------------------------------------------------------------------

type UserStore struct {
	mu      sync.RWMutex
	users   map[int]*User
	version map[int]int
	nextID  int
}

func NewUserStore() *UserStore {
	now := time.Now()
	return &UserStore{
		users: map[int]*User{
			1: {ID: 1, Name: "Alice", Email: "alice@example.com", CreatedAt: now, UpdatedAt: now},
			2: {ID: 2, Name: "Bob", Email: "bob@example.com", CreatedAt: now, UpdatedAt: now},
			3: {ID: 3, Name: "Charlie", Email: "charlie@example.com", CreatedAt: now, UpdatedAt: now},
		},
		version: map[int]int{1: 1, 2: 1, 3: 1},
		nextID:  4,
	}
}

func (s *UserStore) Get(id int) (*User, int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[id]
	if !ok {
		return nil, 0, false
	}
	snapshot := *u
	return &snapshot, s.version[id], true
}

func (s *UserStore) List() ([]*User, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	users := make([]*User, 0, len(s.users))
	h := sha256.New()
	for _, id := range slices.Sorted(maps.Keys(s.users)) {
		snapshot := *s.users[id]
		users = append(users, &snapshot)
		// A hash.Hash write cannot fail. Delimit the IDs and versions.
		_, _ = fmt.Fprintf(h, "%d:%d;", id, s.version[id])
	}
	return users, hex.EncodeToString(h.Sum(nil))
}

func (s *UserStore) Create(name, email string) *User {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	u := &User{ID: s.nextID, Name: name, Email: email, CreatedAt: now, UpdatedAt: now}
	s.users[s.nextID] = u
	s.version[s.nextID] = 1
	s.nextID++
	snapshot := *u
	return &snapshot
}

func (s *UserStore) Update(id int, name, email string) (*User, int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[id]
	if !ok {
		return nil, 0, false
	}
	u.Name = name
	u.Email = email
	u.UpdatedAt = time.Now()
	s.version[id]++
	snapshot := *u
	return &snapshot, s.version[id], true
}

func (s *UserStore) Delete(id int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[id]; !ok {
		return false
	}
	delete(s.users, id)
	delete(s.version, id)
	return true
}

type DeviceStore struct {
	mu      sync.RWMutex
	devices map[int]*Device
	version map[int]int
}

func NewDeviceStore() *DeviceStore {
	now := time.Now()
	return &DeviceStore{
		devices: map[int]*Device{
			1: {ID: 1, Serial: "SN-001", Name: "Temp Sensor A", Status: "active", LastSeen: now},
			2: {ID: 2, Serial: "SN-002", Name: "Humidity Probe", Status: "active", LastSeen: now},
			3: {ID: 3, Serial: "SN-003", Name: "Gateway North", Status: "offline", LastSeen: now.Add(-24 * time.Hour)},
		},
		version: map[int]int{1: 1, 2: 1, 3: 1},
	}
}

func (s *DeviceStore) Get(id int) (*Device, int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.devices[id]
	if !ok {
		return nil, 0, false
	}
	snapshot := *d
	return &snapshot, s.version[id], true
}

func (s *DeviceStore) List() ([]*Device, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	devices := make([]*Device, 0, len(s.devices))
	h := sha256.New()
	for _, id := range slices.Sorted(maps.Keys(s.devices)) {
		snapshot := *s.devices[id]
		devices = append(devices, &snapshot)
		// A hash.Hash write cannot fail. Delimit the IDs and versions.
		_, _ = fmt.Fprintf(h, "%d:%d;", id, s.version[id])
	}
	return devices, hex.EncodeToString(h.Sum(nil))
}

// ---------------------------------------------------------------------------
// Middleware
// ---------------------------------------------------------------------------

// This local demo token is not a production credential.
const demoToken = "local-httpx-demo"

// authMiddleware checks the Authorization: Bearer header.
func authMiddleware(token string) gin.HandlerFunc {
	return func(c *gin.Context) {
		auth := c.GetHeader("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") || subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(auth, "Bearer ")), []byte(token)) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, APIError{
				Code: "UNAUTHORIZED", Message: "invalid or missing bearer token",
			})
			return
		}
		c.Next()
	}
}

// gzipMiddleware covers these buffered JSON routes. It starts a gzip stream
// only when a body is written, so 204 and 304 stay bodyless.
func gzipMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Add("Vary", "Accept-Encoding")
		if c.Request.Method == http.MethodHead || !acceptsGzip(c.Request.Header) {
			c.Next()
			return
		}
		w := &gzipWriter{ResponseWriter: c.Writer}
		c.Writer = w
		c.Next()
		if w.writer != nil {
			if err := w.writer.Close(); err != nil {
				// The response is already committed; log the write failure.
				slog.WarnContext(c.Request.Context(), "finish gzip response", "error", err)
			}
		}
	}
}

func acceptsGzip(header http.Header) bool {
	wildcard := false
	for _, field := range header.Values("Accept-Encoding") {
		for coding := range strings.SplitSeq(field, ",") {
			name, params, err := mime.ParseMediaType(strings.TrimSpace(coding))
			if err != nil {
				continue
			}
			q := 1.0
			if raw, ok := params["q"]; ok {
				q, err = strconv.ParseFloat(raw, 64)
				if err != nil || !(q >= 0 && q <= 1) {
					q = 0
				}
			}
			switch name {
			case "gzip":
				return q > 0
			case "*":
				wildcard = q > 0
			}
		}
	}
	return wildcard
}

type gzipWriter struct {
	gin.ResponseWriter
	writer *gzip.Writer
}

func (g *gzipWriter) Write(data []byte) (int, error) {
	if g.Status() < 200 || g.Status() == http.StatusNoContent || g.Status() == http.StatusNotModified {
		return g.ResponseWriter.Write(data)
	}
	if g.writer == nil {
		g.Header().Set("Content-Encoding", "gzip")
		g.Header().Del("Content-Length")
		g.ResponseWriter.WriteHeaderNow()
		g.writer = gzip.NewWriter(g.ResponseWriter)
	}
	return g.writer.Write(data)
}

func (g *gzipWriter) WriteString(data string) (int, error) { return g.Write([]byte(data)) }

func generateETag(prefix string, id, version int) string {
	// A weak validator identifies the same data across gzip and identity encodings.
	return fmt.Sprintf(`W/"%s-%d-%d"`, prefix, id, version)
}

// ---------------------------------------------------------------------------
// Routes
// ---------------------------------------------------------------------------

func newRouter(token string) *gin.Engine {
	users := NewUserStore()
	devices := NewDeviceStore()

	r := gin.Default()

	// Public: health check (no auth, no caching)
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// All API routes require auth and support gzip.
	api := r.Group("/api", authMiddleware(token), gzipMiddleware())
	{
		// ---------------------------------------------------------------
		// Users
		// ---------------------------------------------------------------

		api.GET("/users", func(c *gin.Context) {
			list, etag := users.List()
			etag = `W/"` + etag + `"`
			c.Header("ETag", etag)
			c.Header("Cache-Control", "private, max-age=10, stale-while-revalidate=30")
			if c.GetHeader("If-None-Match") == etag {
				c.Status(http.StatusNotModified)
				return
			}
			c.JSON(http.StatusOK, list)
		})

		api.GET("/users/:id", func(c *gin.Context) {
			id, err := strconv.Atoi(c.Param("id"))
			if err != nil {
				c.JSON(http.StatusBadRequest, APIError{Code: "BAD_REQUEST", Message: "invalid id"})
				return
			}
			user, version, ok := users.Get(id)
			if !ok {
				c.JSON(http.StatusNotFound, APIError{Code: "NOT_FOUND", Message: "user not found"})
				return
			}
			etag := generateETag("user", id, version)
			c.Header("ETag", etag)
			c.Header("Cache-Control", "private, max-age=60, stale-if-error=3600")
			c.Header("Last-Modified", user.UpdatedAt.UTC().Format(http.TimeFormat))
			if c.GetHeader("If-None-Match") == etag {
				c.Status(http.StatusNotModified)
				return
			}
			c.JSON(http.StatusOK, user)
		})

		api.POST("/users", func(c *gin.Context) {
			var input struct {
				Name  string `json:"name" binding:"required"`
				Email string `json:"email" binding:"required"`
			}
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
			if err := c.ShouldBindJSON(&input); err != nil {
				c.JSON(http.StatusBadRequest, APIError{Code: "VALIDATION", Message: err.Error()})
				return
			}
			user := users.Create(input.Name, input.Email)
			c.Header("Cache-Control", "no-store")
			c.JSON(http.StatusCreated, user)
		})

		api.PUT("/users/:id", func(c *gin.Context) {
			id, err := strconv.Atoi(c.Param("id"))
			if err != nil {
				c.JSON(http.StatusBadRequest, APIError{Code: "BAD_REQUEST", Message: "invalid id"})
				return
			}
			var input struct {
				Name  string `json:"name" binding:"required"`
				Email string `json:"email" binding:"required"`
			}
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
			if err := c.ShouldBindJSON(&input); err != nil {
				c.JSON(http.StatusBadRequest, APIError{Code: "VALIDATION", Message: err.Error()})
				return
			}
			user, _, ok := users.Update(id, input.Name, input.Email)
			if !ok {
				c.JSON(http.StatusNotFound, APIError{Code: "NOT_FOUND", Message: "user not found"})
				return
			}
			c.Header("Cache-Control", "no-store")
			c.JSON(http.StatusOK, user)
		})

		api.DELETE("/users/:id", func(c *gin.Context) {
			id, err := strconv.Atoi(c.Param("id"))
			if err != nil {
				c.JSON(http.StatusBadRequest, APIError{Code: "BAD_REQUEST", Message: "invalid id"})
				return
			}
			if !users.Delete(id) {
				c.JSON(http.StatusNotFound, APIError{Code: "NOT_FOUND", Message: "user not found"})
				return
			}
			c.Header("Cache-Control", "no-store")
			c.Status(http.StatusNoContent) // 204 — no body
		})

		// ---------------------------------------------------------------
		// Devices (second resource type)
		// ---------------------------------------------------------------

		api.GET("/devices", func(c *gin.Context) {
			list, etag := devices.List()
			etag = `W/"` + etag + `"`
			c.Header("ETag", etag)
			c.Header("Cache-Control", "private, max-age=30")
			if c.GetHeader("If-None-Match") == etag {
				c.Status(http.StatusNotModified)
				return
			}
			c.JSON(http.StatusOK, list)
		})

		api.GET("/devices/:id", func(c *gin.Context) {
			id, err := strconv.Atoi(c.Param("id"))
			if err != nil {
				c.JSON(http.StatusBadRequest, APIError{Code: "BAD_REQUEST", Message: "invalid id"})
				return
			}
			device, version, ok := devices.Get(id)
			if !ok {
				c.JSON(http.StatusNotFound, APIError{Code: "NOT_FOUND", Message: "device not found"})
				return
			}
			etag := generateETag("device", id, version)
			c.Header("ETag", etag)
			c.Header("Cache-Control", "private, max-age=30")
			if c.GetHeader("If-None-Match") == etag {
				c.Status(http.StatusNotModified)
				return
			}
			c.JSON(http.StatusOK, device)
		})

		// ---------------------------------------------------------------
		// Flaky endpoint (for retry testing)
		// ---------------------------------------------------------------

		var failCount int
		var failMu sync.Mutex
		api.GET("/flaky", func(c *gin.Context) {
			failMu.Lock()
			failCount++
			count := failCount
			failMu.Unlock()

			if count%3 == 0 {
				c.JSON(http.StatusServiceUnavailable, APIError{
					Code: "UNAVAILABLE", Message: "service temporarily unavailable",
				})
				return
			}
			c.JSON(http.StatusOK, gin.H{"attempt": count})
		})
	}

	return r
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address (keep this demo on loopback)")
	timeout := flag.Duration("timeout", 30*time.Second, "request read and write timeout")
	idleTimeout := flag.Duration("idle-timeout", time.Minute, "idle connection timeout")
	shutdownTimeout := flag.Duration("shutdown-timeout", 5*time.Second, "graceful shutdown timeout")
	flag.Parse()
	if *timeout <= 0 || *idleTimeout <= 0 || *shutdownTimeout <= 0 {
		slog.Error("timeouts must be positive")
		os.Exit(1)
	}
	token := os.Getenv("HTTPX_EXAMPLE_TOKEN")
	if token == "" {
		token = demoToken
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	gin.SetMode(gin.ReleaseMode)
	srv := &http.Server{
		Addr:              *addr,
		Handler:           newRouter(token),
		ReadHeaderTimeout: min(*timeout, 5*time.Second),
		ReadTimeout:       *timeout,
		WriteTimeout:      *timeout,
		IdleTimeout:       *idleTimeout,
		MaxHeaderBytes:    1 << 20,
	}
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe() }()
	slog.Info("example server listening", "address", *addr)
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			slog.Error("serve", "error", err)
			os.Exit(1)
		}
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), *shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Error("shutdown", "error", err)
			os.Exit(1)
		}
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			slog.Error("serve", "error", err)
			os.Exit(1)
		}
	}
}
