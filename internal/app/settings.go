package app

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"strings"

	"tgwsproxy/internal/proxy"
)

//go:embed settings.html
var settingsHTML string

type settingsServer struct {
	ln    net.Listener
	token string
	a     *App
}

func (s *settingsServer) url() string {
	return fmt.Sprintf("http://%s/%s/", s.ln.Addr().String(), s.token)
}

func startSettings(a *App) (*settingsServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	tok := make([]byte, 16)
	rand.Read(tok)
	s := &settingsServer{ln: ln, token: hex.EncodeToString(tok), a: a}
	mux := http.NewServeMux()
	p := "/" + s.token + "/"
	mux.HandleFunc(p, s.page)
	mux.HandleFunc(p+"api/state", s.state)
	mux.HandleFunc(p+"api/config", s.save)
	mux.HandleFunc(p+"api/secret", s.newSecret)
	mux.HandleFunc(p+"api/open", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			OpenURL(a.Link())
		}
	})
	go http.Serve(ln, s.guard(mux))
	return s, nil
}

// guard rejects foreign Host headers (DNS rebinding) and cross-site posts.
func (s *settingsServer) guard(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != s.ln.Addr().String() {
			http.Error(w, "forbidden", 403)
			return
		}
		if r.Method == http.MethodPost {
			if o := r.Header.Get("Origin"); o != "" && o != "http://"+s.ln.Addr().String() {
				http.Error(w, "forbidden", 403)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})
}

func (s *settingsServer) page(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/"+s.token+"/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'")
	lang := s.a.Config().Language
	fmt.Fprint(w, strings.ReplaceAll(settingsHTML, "{{LANG}}", lang))
}

type stateResp struct {
	Config    Config `json:"config"`
	Link      string `json:"link"`
	Running   bool   `json:"running"`
	Error     string `json:"error"`
	Autostart bool   `json:"autostart"`
	OS        string `json:"os"`
	Stats     struct {
		Active, Total, WS, CF, CFW, TCP, Stalls, PoolHits, PoolMisses int64
		Up, Down                                                      string
	} `json:"stats"`
}

func (s *settingsServer) buildState() stateResp {
	var st stateResp
	st.Config = s.a.Config()
	st.Link = s.a.Link()
	st.OS = runtime.GOOS
	st.Autostart = GetAutostart()
	running, errText, stats := s.a.Status()
	st.Running, st.Error = running, errText
	if stats != nil {
		st.Stats.Active = stats.ConnActive.Load()
		st.Stats.Total = stats.ConnTotal.Load()
		st.Stats.WS = stats.ConnWS.Load()
		st.Stats.CF = stats.ConnCF.Load()
		st.Stats.CFW = stats.ConnCFWorker.Load()
		st.Stats.TCP = stats.ConnTCP.Load()
		st.Stats.Stalls = stats.Stalls.Load()
		st.Stats.PoolHits = stats.PoolHits.Load()
		st.Stats.PoolMisses = stats.PoolMisses.Load()
		st.Stats.Up = proxy.HumanBytes(stats.BytesUp.Load())
		st.Stats.Down = proxy.HumanBytes(stats.BytesDown.Load())
	}
	return st
}

func (s *settingsServer) state(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.buildState())
}

func (s *settingsServer) save(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", 405)
		return
	}
	var c Config
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&c); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := s.a.Apply(c); err != nil {
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(s.buildState())
}

func (s *settingsServer) newSecret(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", 405)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"secret": hex.EncodeToString(randomSecret())})
}
