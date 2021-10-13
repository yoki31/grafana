package rendering

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/grafana/grafana/pkg/infra/log"
	"github.com/grafana/grafana/pkg/setting"
	"github.com/stretchr/testify/require"
)

func TestGetUrl(t *testing.T) {
	path := "render/d-solo/5SdHCadmz/panel-tests-graph?orgId=1&from=1587390211965&to=1587393811965&panelId=5&width=1000&height=500&tz=Europe%2FStockholm"
	cfg := setting.NewCfg()
	rs := &RenderingService{
		Cfg: cfg,
	}

	t.Run("When renderer and callback url configured should return callback url plus path", func(t *testing.T) {
		rs.Cfg.RendererUrl = "http://localhost:8081/render"
		rs.Cfg.RendererCallbackUrl = "http://public-grafana.com/"
		url := rs.getURL(path)
		require.Equal(t, rs.Cfg.RendererCallbackUrl+path+"&render=1", url)
	})

	t.Run("When renderer url not configured", func(t *testing.T) {
		rs.Cfg.RendererUrl = ""
		rs.domain = "localhost"
		rs.Cfg.HTTPPort = "3000"

		t.Run("And protocol HTTP configured should return expected path", func(t *testing.T) {
			rs.Cfg.ServeFromSubPath = false
			rs.Cfg.AppSubURL = ""
			rs.Cfg.Protocol = setting.HTTPScheme
			url := rs.getURL(path)
			require.Equal(t, "http://localhost:3000/"+path+"&render=1", url)

			t.Run("And serve from sub path should return expected path", func(t *testing.T) {
				rs.Cfg.ServeFromSubPath = true
				rs.Cfg.AppSubURL = "/grafana"
				url := rs.getURL(path)
				require.Equal(t, "http://localhost:3000/grafana/"+path+"&render=1", url)
			})
		})

		t.Run("And protocol HTTPS configured should return expected path", func(t *testing.T) {
			rs.Cfg.ServeFromSubPath = false
			rs.Cfg.AppSubURL = ""
			rs.Cfg.Protocol = setting.HTTPSScheme
			url := rs.getURL(path)
			require.Equal(t, "https://localhost:3000/"+path+"&render=1", url)
		})

		t.Run("And protocol HTTP2 configured should return expected path", func(t *testing.T) {
			rs.Cfg.ServeFromSubPath = false
			rs.Cfg.AppSubURL = ""
			rs.Cfg.Protocol = setting.HTTP2Scheme
			url := rs.getURL(path)
			require.Equal(t, "https://localhost:3000/"+path+"&render=1", url)
		})
	})
}

func TestRenderingService404Behavior(t *testing.T) {
	cfg := setting.NewCfg()
	rs := &RenderingService{
		Cfg: cfg,
		log: log.New("rendering-test"),
	}

	t.Run("When renderer responds with correct version should return that version", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("{\"version\":\"2.7.1828\"}"))
		}))
		defer server.Close()

		rs.Cfg.RendererUrl = server.URL + "/render"
		version, err := rs.getRemotePluginVersion()

		require.NoError(t, err)
		require.Equal(t, version, "2.7.1828")
	})

	t.Run("When renderer responds with 404 should assume a valid but old version", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		rs.Cfg.RendererUrl = server.URL + "/render"
		version, err := rs.getRemotePluginVersion()

		require.NoError(t, err)
		require.Equal(t, version, "1.0.0")
	})
}
