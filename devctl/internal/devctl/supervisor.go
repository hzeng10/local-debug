package devctl

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

type request struct {
	Profile   string `json:"profile,omitempty"`
	Transport string `json:"transport,omitempty"`
	SSHHost   string `json:"sshHost,omitempty"`
	Name      string `json:"name,omitempty"`
	Suite     string `json:"suite,omitempty"`
}
type AppStatus struct {
	Name         string `json:"name"`
	State        string `json:"state"`
	PID          int    `json:"pid,omitempty"`
	HealthURL    string `json:"healthURL"`
	LogPath      string `json:"logPath"`
	ExitCode     int    `json:"exitCode,omitempty"`
	ConfigDigest string `json:"configDigest"`
}
type Status struct {
	Network   string      `json:"network"`
	Message   string      `json:"message,omitempty"`
	Backend   string      `json:"backend"`
	Transport string      `json:"transport"`
	Profiles  []AppStatus `json:"profiles"`
}
type application struct {
	prepared Prepared
	process  *Process
	state    string
	test     *Process
}
type supervisor struct {
	mu         sync.Mutex
	prepareMu  sync.Mutex
	apps       map[string]*application
	link       *Link
	profile    Profile
	root       string
	sessionDir string
	token      string
	ctx        context.Context
	cancel     context.CancelFunc
}

func Serve(p Profile, root string) error {
	if err := secureDir(root); err != nil {
		return err
	}
	lock := filepath.Join(root, "network.lock")
	if err := os.Mkdir(lock, 0700); err != nil {
		return fmt.Errorf("another session owns network.lock; use status/disconnect, or inspect stale state before manual recovery")
	}
	defer os.RemoveAll(lock)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	bootstrapTimer := time.AfterFunc(120*time.Second, cancel)
	defer bootstrapTimer.Stop()
	s := &supervisor{profile: p, root: root, token: token(), apps: map[string]*application{}, ctx: ctx, cancel: cancel}
	s.sessionDir = filepath.Join(root, "sessions", s.token[:16])
	if err := secureDir(s.sessionDir); err != nil {
		return err
	}
	defer func() {
		s.mu.Lock()
		for _, a := range s.apps {
			if a.process != nil {
				a.process.Stop()
				<-a.process.done
			}
			if a.test != nil {
				a.test.Stop()
				<-a.test.done
			}
			_ = os.RemoveAll(a.prepared.Dir)
		}
		s.mu.Unlock()
		if s.link != nil {
			if err := s.link.Close(); err != nil {
				_ = writePrivate(filepath.Join(root, "cleanup-error.txt"), []byte(err.Error()))
			}
		}
	}()
	if err := s.prepare(ctx, p); err != nil {
		return err
	}
	link, err := StartLink(ctx, p, filepath.Join(s.sessionDir, "network"))
	if err != nil {
		return err
	}
	s.link = link
	bootstrapTimer.Stop()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &http.Server{Handler: s.handler(), ReadHeaderTimeout: 5 * time.Second}
	state := Session{URL: "http://" + listener.Addr().String(), Token: s.token, PID: os.Getpid()}
	if err = writeJSON(filepath.Join(root, "session.json"), state); err != nil {
		return err
	}
	defer os.Remove(filepath.Join(root, "session.json"))
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
		_ = server.Shutdown(shutdown)
		stop()
		_ = server.Close()
		return nil
	case err := <-done:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}
func (s *supervisor) prepare(ctx context.Context, p Profile) error {
	s.prepareMu.Lock()
	defer s.prepareMu.Unlock()
	if p.NetworkKey() != s.profile.NetworkKey() {
		return fmt.Errorf("another network configuration is active; disconnect before changing cluster or backend")
	}
	s.mu.Lock()
	old := s.apps[p.Name]
	if old != nil && ((old.process != nil && old.process.Running()) || (old.test != nil && old.test.Running())) {
		s.mu.Unlock()
		return fmt.Errorf("stop the profile before refreshing its configuration")
	}
	s.mu.Unlock()
	dir := filepath.Join(s.sessionDir, p.Name+"-"+token()[:8])
	prepared, err := Prepare(ctx, p, Kube{p}, dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return err
	}
	s.mu.Lock()
	s.apps[p.Name] = &application{prepared: prepared, state: "prepared"}
	s.mu.Unlock()
	if old != nil {
		_ = os.RemoveAll(old.prepared.Dir)
	}
	return nil
}
func (s *supervisor) snapshot() Status {
	state, message := s.link.State()
	out := Status{Network: state, Message: message, Backend: s.profile.Network.Backend, Transport: s.profile.Network.Transport, Profiles: []AppStatus{}}
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, a := range s.apps {
		v := AppStatus{Name: name, State: a.state, HealthURL: a.prepared.Profile.Application.HealthURL, LogPath: filepath.Join(s.sessionDir, name+".log"), ConfigDigest: a.prepared.Profile.Digest()}
		if a.process != nil {
			if a.process.Running() {
				v.PID = a.process.cmd.Process.Pid
				if state != "connected" {
					v.State = "network-unavailable"
				}
			} else {
				v.State = "exited"
				v.ExitCode = a.process.ExitCode()
			}
		}
		out.Profiles = append(out.Profiles, v)
	}
	sort.Slice(out.Profiles, func(i, j int) bool { return out.Profiles[i].Name < out.Profiles[j].Name })
	return out
}
func (s *supervisor) run(ctx context.Context, name string) error {
	s.prepareMu.Lock()
	defer s.prepareMu.Unlock()
	if state, _ := s.link.State(); state != "connected" {
		return fmt.Errorf("network is not connected")
	}
	s.mu.Lock()
	a := s.apps[name]
	if a == nil {
		s.mu.Unlock()
		return fmt.Errorf("profile not prepared; run connect first")
	}
	if a.process != nil && a.process.Running() {
		s.mu.Unlock()
		return fmt.Errorf("profile is already running")
	}
	p := a.prepared
	// A pre-existing health listener must never make a failed new JVM look healthy.
	u, err := netURLAddress(p.Profile.Application.HealthURL)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if probeTCP(ctx, u) == nil {
		s.mu.Unlock()
		return fmt.Errorf("application health port is already in use")
	}
	proc, err := StartProcess(p.Profile.Application.Command, p.Profile.Application.WorkDir, appEnvironment(p), filepath.Join(s.sessionDir, name+".log"), p.Secrets)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	a.process = proc
	a.state = "starting"
	s.mu.Unlock()
	limited, cancel := context.WithTimeout(ctx, time.Duration(p.Profile.Application.StartupSeconds)*time.Second)
	defer cancel()
	go func() {
		select {
		case <-proc.done:
			cancel()
		case <-limited.Done():
		}
	}()
	err = retryUntil(limited, 500*time.Millisecond, func() error {
		if !proc.Running() {
			return fmt.Errorf("application exited before becoming ready")
		}
		if state, _ := s.link.State(); state != "connected" {
			return fmt.Errorf("network is unavailable")
		}
		return probeHTTP(limited, p.Profile.Application.HealthURL, p.Profile.Application.HealthStatus)
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		proc.Stop()
		a.state = "failed"
		return fmt.Errorf("application did not become ready; inspect redacted application log")
	}
	a.state = "running"
	return nil
}
func (s *supervisor) stop(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.apps[name]
	if a == nil {
		return fmt.Errorf("profile not prepared")
	}
	if a.process != nil {
		a.process.Stop()
		<-a.process.done
		a.process = nil
	}
	if a.test != nil {
		a.test.Stop()
		<-a.test.done
		a.test = nil
	}
	a.state = "prepared"
	return nil
}
func (s *supervisor) test(ctx context.Context, name, suite string) (map[string]any, error) {
	if state, _ := s.link.State(); state != "connected" {
		return nil, fmt.Errorf("network is unavailable")
	}
	s.mu.Lock()
	a := s.apps[name]
	if a == nil || a.process == nil || !a.process.Running() || a.state != "running" {
		s.mu.Unlock()
		return nil, fmt.Errorf("application is not ready")
	}
	if a.test != nil && a.test.Running() {
		s.mu.Unlock()
		return nil, fmt.Errorf("a test suite is already running")
	}
	argv, ok := a.prepared.Profile.Application.Tests[suite]
	if !ok || len(argv) == 0 {
		s.mu.Unlock()
		return nil, fmt.Errorf("unknown test suite")
	}
	path := filepath.Join(s.sessionDir, name+"-test-"+token()[:8]+".log")
	proc, err := StartProcess(argv, a.prepared.Profile.Application.WorkDir, appEnvironment(a.prepared), path, a.prepared.Secrets)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	a.test = proc
	timeout := a.prepared.Profile.Application.TestTimeoutSeconds
	s.mu.Unlock()
	limited, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	err = waitProcess(limited, proc)
	report := map[string]any{"suite": suite, "profile": name, "exitCode": proc.ExitCode(), "logPath": path, "passed": err == nil}
	if state, _ := s.link.State(); state != "connected" {
		err = fmt.Errorf("network became unavailable during test")
		report["passed"] = false
	}
	_ = writeJSON(strings.TrimSuffix(path, ".log")+".json", report)
	return report, err
}

func (s *supervisor) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.ctx != nil {
			ctx, cancel := context.WithCancel(r.Context())
			stop := context.AfterFunc(s.ctx, cancel)
			defer func() { stop(); cancel() }()
			r = r.WithContext(ctx)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method != "POST" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+s.token)) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var q request
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024*1024))
		d.DisallowUnknownFields()
		if err := d.Decode(&q); err != nil {
			w.WriteHeader(400)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid request"})
			return
		}
		var result any
		var err error
		switch r.URL.Path {
		case "/status":
			result = s.snapshot()
		case "/prepare":
			var p Profile
			p, err = LoadProfile(q.Profile, q.Transport, q.SSHHost)
			if err == nil {
				err = s.prepare(r.Context(), p)
			}
			if err == nil {
				result = s.snapshot()
			}
		case "/run":
			err = s.run(r.Context(), q.Name)
			if err == nil {
				result = s.snapshot()
			}
		case "/stop":
			err = s.stop(q.Name)
			if err == nil {
				result = s.snapshot()
			}
		case "/test":
			result, err = s.test(r.Context(), q.Name, q.Suite)
		case "/probe":
			s.mu.Lock()
			a := s.apps[q.Name]
			s.mu.Unlock()
			if a == nil {
				err = fmt.Errorf("profile not prepared")
			} else {
				result = probeDependencies(r.Context(), a.prepared.Profile)
			}
		case "/disconnect":
			result = map[string]bool{"disconnecting": true}
			defer s.cancel()
		default:
			w.WriteHeader(404)
			return
		}
		if err != nil {
			w.WriteHeader(http.StatusConflict)
			result = map[string]any{"error": err.Error(), "result": result}
		}
		_ = json.NewEncoder(w).Encode(result)
		if r.URL.Path == "/disconnect" {
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	})
}
func call(ctx context.Context, root, path string, q request) (json.RawMessage, error) {
	s, err := loadSession(root)
	if err != nil {
		return nil, fmt.Errorf("no active devctl session; run connect")
	}
	// State is local, but validate it before sending a bearer token anywhere.
	u, parseErr := url.Parse(s.URL)
	if parseErr != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("invalid control endpoint")
	}
	b, _ := json.Marshal(q)
	req, err := http.NewRequestWithContext(ctx, "POST", s.URL+path, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("local supervisor is unavailable; inspect %s before recovering stale state", root)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		var v struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &v)
		if v.Error == "" {
			v.Error = "supervisor request rejected"
		}
		return body, fmt.Errorf("%s", v.Error)
	}
	return body, nil
}
