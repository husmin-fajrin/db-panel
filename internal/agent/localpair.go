package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/husmin-fajrin/db-panel/internal/agent/config"
)

// LocalPairTokenFile is the file install.sh puts in the agent directory of
// the panel host: the token the panel issued for its own server and the
// SPKI pin of the panel's HTTPS certificate, "<token>\n<pin>\n".
const LocalPairTokenFile = "local-pair-token"

// localPanelURL is where the panel host's agent reaches its panel to pair.
const localPanelURL = "https://127.0.0.1:8080"

// maxTokenFile caps what is read from a token file.
const maxTokenFile = 4 << 10

// readTokenFile reads a token file: the token on the first line and the
// web pin on the second.
func readTokenFile(path string) (token, pin string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", fmt.Errorf("baca berkas token: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(io.LimitReader(f, maxTokenFile))
	var lines []string
	for sc.Scan() && len(lines) < 2 {
		lines = append(lines, strings.TrimSpace(sc.Text()))
	}
	if err := sc.Err(); err != nil {
		return "", "", fmt.Errorf("baca berkas token %s: %w", path, err)
	}
	if len(lines) < 2 || lines[0] == "" || !validSPKIPin(lines[1]) {
		return "", "", fmt.Errorf("berkas token %s harus berisi token lalu pin sertifikat panel (sha256//...), satu per baris", path)
	}
	return lines[0], lines[1], nil
}

// pairFromFile pairs with the token and pin of the token file at path and
// deletes the file once the agent is paired (the token is used up). The
// pin authenticates the panel, so the CA in its answer is trusted.
func pairFromFile(ctx context.Context, panelURL, path, dir, agentAddr string) (config.Config, error) {
	token, pin, err := readTokenFile(path)
	if err != nil {
		return config.Config{}, err
	}
	cfg, err := Pair(ctx, panelURL, token, dir, nil, PairOptions{WebPin: pin, AgentAddr: agentAddr})
	if err != nil {
		return config.Config{}, err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return cfg, fmt.Errorf("agent terpasang, tetapi berkas token gagal dihapus: %w", err)
	}
	return cfg, nil
}

// needsLocalPair reports whether dir holds install.sh's token file and no
// agent.yaml yet: the panel host's agent pairs before it runs.
func needsLocalPair(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, LocalPairTokenFile)); err != nil {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, config.FileName))
	return errors.Is(err, os.ErrNotExist)
}
