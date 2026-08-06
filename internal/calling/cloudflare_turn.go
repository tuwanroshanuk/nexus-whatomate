package calling

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

const (
	cloudflareTURNEndpoint = "https://rtc.live.cloudflare.com/v1/turn/keys/%s/credentials/generate-ice-servers"
	defaultCloudflareTTL   = 3600
)

type cloudflareTURNCache struct {
	mu        sync.Mutex
	servers   []webrtc.ICEServer
	expiresAt time.Time
}

var cfTURNCache cloudflareTURNCache

type cloudflareTURNResponse struct {
	ICEServers []struct {
		URLs       []string `json:"urls"`
		Username   string   `json:"username"`
		Credential string   `json:"credential"`
	} `json:"iceServers"`
}

// resolveICEServers combines statically configured ICE servers with optional
// short-lived Cloudflare Realtime TURN credentials. Cloudflare secrets stay on
// the server and are never exposed to the browser.
func (m *Manager) resolveICEServers(now time.Time) ([]webrtc.ICEServer, error) {
	servers := make([]webrtc.ICEServer, 0, len(m.config.ICEServers)+2)
	for _, s := range m.config.ICEServers {
		ice := webrtc.ICEServer{URLs: s.URLs}
		if username, credential := s.ResolveCredentials(now); username != "" {
			ice.Username = username
			ice.Credential = credential
			ice.CredentialType = webrtc.ICECredentialTypePassword
		}
		servers = append(servers, ice)
	}

	keyID := strings.TrimSpace(os.Getenv("WHATOMATE_CALLING__CLOUDFLARE_TURN_KEY_ID"))
	apiToken := strings.TrimSpace(os.Getenv("WHATOMATE_CALLING__CLOUDFLARE_TURN_API_TOKEN"))
	if keyID == "" && apiToken == "" {
		return servers, nil
	}
	if keyID == "" || apiToken == "" {
		return nil, fmt.Errorf("Cloudflare TURN requires both key ID and API token")
	}

	cfServers, err := getCloudflareTURNServers(now, keyID, apiToken)
	if err != nil {
		return nil, err
	}
	servers = append(servers, cfServers...)
	return servers, nil
}

func getCloudflareTURNServers(now time.Time, keyID, apiToken string) ([]webrtc.ICEServer, error) {
	cfTURNCache.mu.Lock()
	defer cfTURNCache.mu.Unlock()

	if len(cfTURNCache.servers) > 0 && now.Before(cfTURNCache.expiresAt.Add(-5*time.Minute)) {
		return append([]webrtc.ICEServer(nil), cfTURNCache.servers...), nil
	}

	ttl := defaultCloudflareTTL
	if raw := strings.TrimSpace(os.Getenv("WHATOMATE_CALLING__CLOUDFLARE_TURN_TTL")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 600 {
			return nil, fmt.Errorf("invalid Cloudflare TURN TTL %q; use at least 600 seconds", raw)
		}
		ttl = parsed
	}

	body, _ := json.Marshal(map[string]int{"ttl": ttl})
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf(cloudflareTURNEndpoint, keyID), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create Cloudflare TURN request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiToken)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("generate Cloudflare TURN credentials: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Cloudflare TURN credential API returned HTTP %d", resp.StatusCode)
	}

	var payload cloudflareTURNResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode Cloudflare TURN credentials: %w", err)
	}
	if len(payload.ICEServers) == 0 {
		return nil, fmt.Errorf("Cloudflare TURN credential API returned no ICE servers")
	}

	servers := make([]webrtc.ICEServer, 0, len(payload.ICEServers))
	for _, s := range payload.ICEServers {
		if len(s.URLs) == 0 {
			continue
		}
		ice := webrtc.ICEServer{
			URLs:           s.URLs,
			Username:       s.Username,
			Credential:     s.Credential,
			CredentialType: webrtc.ICECredentialTypePassword,
		}
		servers = append(servers, ice)
	}
	if len(servers) == 0 {
		return nil, fmt.Errorf("Cloudflare TURN response contained no usable ICE servers")
	}

	cfTURNCache.servers = append([]webrtc.ICEServer(nil), servers...)
	cfTURNCache.expiresAt = now.Add(time.Duration(ttl) * time.Second)
	return append([]webrtc.ICEServer(nil), servers...), nil
}
