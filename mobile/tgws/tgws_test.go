//go:build tgwsmobile || android || ios

package tgws

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// Exercises the phone API end to end on the host: init, settings page,
// start/stop, saving settings while stopped must not start the proxy.
func TestMobileLifecycle(t *testing.T) {
	dir := t.TempDir()
	if err := Init(dir, "ru"); err != nil {
		t.Fatal(err)
	}
	if IsRunning() {
		t.Fatal("must not run before StartProxy")
	}
	u, err := SettingsURL()
	if err != nil || !strings.HasPrefix(u, "http://127.0.0.1:") {
		t.Fatalf("settings url %q %v", u, err)
	}
	if _, err := os.Stat(dir + "/config.json"); err != nil {
		t.Fatal("config not created in data dir")
	}
	if err := StartProxy(); err != nil {
		t.Fatal(err)
	}
	if !IsRunning() || !strings.HasPrefix(Link(), "tg://proxy?server=127.0.0.1&port=1443&secret=dd") {
		t.Fatalf("running=%v link=%q", IsRunning(), Link())
	}
	if !strings.Contains(StatusLine(), "Соединений") {
		t.Fatalf("status %q", StatusLine())
	}
	StopProxy()
	if IsRunning() {
		t.Fatal("still running")
	}

	// save settings via the page API while stopped
	r, err := http.Get(u + "api/state")
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		Config map[string]any `json:"config"`
		OS     string         `json:"os"`
	}
	json.NewDecoder(r.Body).Decode(&st)
	r.Body.Close()
	st.Config["port"] = 1499
	st.Config["autostart"] = true
	b, _ := json.Marshal(st.Config)
	resp, err := http.Post(u+"api/config", "application/json", strings.NewReader(string(b)))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("save: %v %v", err, resp.Status)
	}
	time.Sleep(100 * time.Millisecond)
	if IsRunning() {
		t.Fatal("saving settings started a stopped proxy")
	}
	if !strings.Contains(Link(), "port=1499") {
		t.Fatalf("link %q", Link())
	}
	if !AutostartEnabled(dir) {
		t.Fatal("autostart flag not saved")
	}
}
