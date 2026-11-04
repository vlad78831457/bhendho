package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"testing"

	"offgrid/core/internal/core/contracts"
)

// ADR-82: ядро говорит версию договора шлюза из самого файла договора; чужая старшая версия — 409 gateway_version;
// версия Python SDK — та же, что у договора.
func TestGatewayVersionIsSpokenAndChecked(t *testing.T) {
	want, err := contracts.InterfaceVersion(contracts.GatewayFile)
	if err != nil {
		t.Fatal(err)
	}
	h := gatewayVersion(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	for _, tc := range []struct {
		worker string
		status int
	}{{"", http.StatusNoContent}, {want, http.StatusNoContent}, {contracts.Major(want) + ".99.0", http.StatusNoContent}, {"9.0.0", http.StatusConflict}} {
		req := httptest.NewRequest("POST", "/gateway/v1/claim", nil)
		if tc.worker != "" {
			req.Header.Set("X-Worker-Gateway", tc.worker)
		}
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != tc.status || rec.Header().Get("X-Core-Gateway") != want {
			t.Fatalf("worker %q: status %d, X-Core-Gateway %q; want %d, %q", tc.worker, rec.Code, rec.Header().Get("X-Core-Gateway"), tc.status, want)
		}
	}
	raw, err := os.ReadFile("../../../pkg/sdk/python/pyproject.toml")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^version = "([^"]+)"`).FindSubmatch(raw)
	if m == nil || string(m[1]) != want {
		t.Fatalf("версия Python SDK %q — должна совпадать с договором шлюза %q", m, want)
	}
	client, err := os.ReadFile("../../../pkg/sdk/python/src/offgrid_worker/client.py")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`GATEWAY_CONTRACT = "` + regexp.QuoteMeta(want) + `"`).Match(client) {
		t.Fatalf("GATEWAY_CONTRACT в SDK — не %s", want)
	}
}
