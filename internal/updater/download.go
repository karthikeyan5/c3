package updater

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

const (
	// checkTimeout bounds a release-API check (a small JSON GET).
	checkTimeout = 20 * time.Second
	// assetDownloadTimeout bounds a whole tarball download. Tarballs are ~10–30
	// MiB; 10 min tolerates a slow link without hanging forever.
	assetDownloadTimeout = 10 * time.Minute
	// maxAsset caps a downloaded asset so a hostile/mislabelled URL can't fill
	// the disk. C3 tarballs are tens of MiB; 500 MiB is far above any real one.
	maxAsset = 500 << 20
)

// httpsOnlyRedirect rejects any redirect hop that leaves https. requireHTTPS
// validates the INITIAL url; without this policy Go's default client would
// happily follow an https→http downgrade redirect, quietly voiding the
// https-only invariant. The download is checksum-gated regardless, so this is
// defense-in-depth, but it makes the stated guarantee actually hold.
func httpsOnlyRedirect(req *http.Request, _ []*http.Request) error {
	if req.URL.Scheme != "https" {
		return fmt.Errorf("refusing redirect to non-https url %q", req.URL.String())
	}
	return nil
}

// DefaultClient returns the HTTP client used for the release-API CHECK: a
// conservative whole-request timeout, and http.DefaultTransport so a configured
// HTTP(S)_PROXY / NO_PROXY in the environment is honoured automatically.
func DefaultClient() *http.Client {
	return &http.Client{Timeout: checkTimeout, CheckRedirect: httpsOnlyRedirect}
}

// downloadClient returns the client for asset DOWNLOADS — same proxy-aware
// default transport, but a much longer timeout than the API check allows.
func downloadClient() *http.Client {
	return &http.Client{Timeout: assetDownloadTimeout, CheckRedirect: httpsOnlyRedirect}
}

// downloadClientFn is the factory Update uses to build its asset-download client.
// A package var (defaulting to downloadClient) purely so tests can substitute a
// client that trusts a loopback TLS test server; production never reassigns it.
var downloadClientFn = downloadClient

// requireHTTPS rejects any non-https URL before a request is made. Release asset
// URLs from GitHub are always https; refusing anything else keeps a tampered API
// response from redirecting the binary download to a plaintext host.
func requireHTTPS(rawurl string) error {
	u, err := url.Parse(rawurl)
	if err != nil {
		return fmt.Errorf("parse url %q: %w", rawurl, err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("refusing non-https download url %q", rawurl)
	}
	return nil
}

// downloadTo streams rawurl (which MUST be https) into destPath, capping the
// body at maxAsset. It writes directly to destPath (which lives in a private
// temp workdir the caller owns and cleans up); atomicity is not needed here
// because the file is only consumed after this returns and the checksum passes.
func downloadTo(ctx context.Context, client *http.Client, rawurl, destPath string) error {
	if err := requireHTTPS(rawurl); err != nil {
		return err
	}
	if client == nil {
		client = downloadClient()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawurl, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "c3-updater")
	// GitHub asset download URLs serve the raw octet-stream on this Accept.
	req.Header.Set("Accept", "application/octet-stream")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: status %d", rawurl, resp.StatusCode)
	}
	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxAsset+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(destPath)
		return fmt.Errorf("download %s: %w", rawurl, err)
	}
	if n > maxAsset {
		_ = os.Remove(destPath)
		return fmt.Errorf("download %s: exceeds %d-byte cap", rawurl, int64(maxAsset))
	}
	return nil
}
