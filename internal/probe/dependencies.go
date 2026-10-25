package probe

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func parseRegistryToken(body []byte) (string, error) {
	if len(body) > 65536 {
		return "", errors.New("token response too large")
	}
	var v struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
		Expires     *int   `json:"expires_in"`
	}
	if json.Unmarshal(body, &v) != nil {
		return "", errors.New("invalid token response")
	}
	if v.Token == "" {
		v.Token = v.AccessToken
	}
	if v.Token == "" || len(v.Token) > 16384 || strings.IndexFunc(v.Token, func(r rune) bool { return r <= 32 || r >= 127 }) >= 0 || (v.Expires != nil && *v.Expires <= 0) {
		return "", errors.New("invalid token response")
	}
	return v.Token, nil
}

// The token realm is configured, never taken from a response challenge. No token
// values enter results, logs, tracing attributes, errors or the public snapshot.
func (p *Prober) registryToken(ctx context.Context, tokenURL string) (string, string) {
	req, err := http.NewRequestWithContext(ctx, "GET", tokenURL, nil)
	if err != nil {
		return "", "registry auth configuration invalid"
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "public-api-monitor/0.1")
	resp, err := p.Client.Do(req)
	if err != nil {
		return "", "registry auth request failed"
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", "registry auth http " + strconv.Itoa(resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(body) > 65536 {
		return "", "registry auth response unreadable"
	}
	token, err := parseRegistryToken(body)
	if err != nil {
		return "", "registry auth response invalid"
	}
	return token, ""
}

// ApplyRegistryAuth keeps the temporary bearer token inside the request only.
func (p *Prober) ApplyRegistryAuth(req *http.Request, tokenURL string) error {
	if tokenURL == "" {
		return nil
	}
	token, why := p.registryToken(req.Context(), tokenURL)
	if why != "" {
		return errors.New(why)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

func classifyManifest(o Observation, limit time.Duration) Verdict {
	v := classifyHTTP(o, limit)
	if !v.Status.Healthy() {
		return v
	}
	var m struct {
		Schema    int    `json:"schemaVersion"`
		MediaType string `json:"mediaType"`
		Manifests []struct {
			Digest string `json:"digest"`
			Size   int64  `json:"size"`
		} `json:"manifests"`
	}
	valid := json.Unmarshal(o.Body, &m) == nil && m.Schema == 2 && len(m.Manifests) > 0 && (m.MediaType == "application/vnd.oci.image.index.v1+json" || m.MediaType == "application/vnd.docker.distribution.manifest.list.v2+json")
	for _, entry := range m.Manifests {
		digest, err := hex.DecodeString(strings.TrimPrefix(entry.Digest, "sha256:"))
		if !strings.HasPrefix(entry.Digest, "sha256:") || err != nil || len(digest) != 32 || entry.Size <= 0 {
			valid = false
		}
	}
	if !valid {
		return Verdict{Status: StatusError, Reason: "registry manifest: invalid image index"}
	}
	return v
}

// Validate Git smart-HTTP v0 ref discovery, not just an HTTP 200 login/error page.
// This tests ls-remote discovery, not fetching objects or pushing commits.
func classifyGit(o Observation, limit time.Duration) Verdict {
	v := classifyHTTP(o, limit)
	if !v.Status.Healthy() {
		return v
	}
	contentType, _, err := mime.ParseMediaType(o.ContentType)
	if err != nil || contentType != "application/x-git-upload-pack-advertisement" {
		return Verdict{Status: StatusError, Reason: "git: invalid advertisement content type"}
	}
	pos := 0
	read := func() ([]byte, bool, bool) {
		if pos+4 > len(o.Body) {
			return nil, false, false
		}
		n, err := strconv.ParseUint(string(o.Body[pos:pos+4]), 16, 16)
		pos += 4
		if err != nil {
			return nil, false, false
		}
		if n == 0 {
			return nil, true, true
		}
		if n < 4 || pos+int(n)-4 > len(o.Body) {
			return nil, false, false
		}
		b := o.Body[pos : pos+int(n)-4]
		pos += int(n) - 4
		return b, false, true
	}
	first, flush, ok := read()
	if !ok || flush || !bytes.Equal(first, []byte("# service=git-upload-pack\n")) {
		return Verdict{Status: StatusError, Reason: "git: invalid service advertisement"}
	}
	_, flush, ok = read()
	if !ok || !flush {
		return Verdict{Status: StatusError, Reason: "git: missing service flush"}
	}
	head, complete := false, false
	for pos < len(o.Body) {
		packet, flush, ok := read()
		if !ok {
			break
		}
		if flush {
			complete = pos == len(o.Body)
			break
		}
		ref, _, _ := bytes.Cut(packet, []byte{0})
		fields := strings.Fields(string(ref))
		if len(fields) != 2 {
			break
		}
		oid, err := hex.DecodeString(fields[0])
		if err != nil || (len(oid) != 20 && len(oid) != 32) {
			break
		}
		if fields[1] == "HEAD" {
			head = true
		}
	}
	if !head || !complete {
		return Verdict{Status: StatusError, Reason: "git: incomplete or invalid ref advertisement"}
	}
	return v
}
