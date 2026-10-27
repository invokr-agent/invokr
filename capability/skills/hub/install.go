package hub

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/invokr-ai/invokr/capability/skills"
)

const urlFetchTimeout = 30 * time.Second
const urlFetchMaxBytes = 1 << 20 // a SKILL.md is text; 1MiB is generous

// Install resolves ref — an existing local directory, an https:// URL to a
// SKILL.md (name required), or a skill name looked up across the configured
// hubs — and installs it into the skills directory. It returns the installed
// skill's name.
func (s *Service) Install(ctx context.Context, ref string, name string) (string, error) {
	if info, err := os.Stat(ref); err == nil && info.IsDir() {
		return s.installDir(ref, name)
	}
	if strings.HasPrefix(ref, "https://") && strings.HasSuffix(ref, skillFilename) {
		return s.installURL(ctx, ref, name)
	}
	if strings.Contains(ref, "://") {
		return "", fmt.Errorf("unsupported skill source %q: use an https:// URL ending in %s, a local directory, or a hub skill name", ref, skillFilename)
	}
	found, errs := s.Skills(ctx, true)
	if i := slices.IndexFunc(found, func(hs HubSkill) bool { return hs.Name == ref }); i >= 0 {
		return s.installDir(found[i].Dir, "")
	}
	if len(errs) > 0 {
		return "", fmt.Errorf("skill %q not found; hub errors: %v", ref, errs)
	}
	return "", fmt.Errorf("skill %q not found in hubs %s", ref, strings.Join(s.hubs, ", "))
}

// InstallAll refreshes every hub and installs each skill found. It returns
// the installed names and any per-hub or per-skill errors.
func (s *Service) InstallAll(ctx context.Context) ([]string, []error) {
	found, errs := s.Skills(ctx, true)
	var names []string
	for _, hs := range found {
		if _, err := s.installDir(hs.Dir, ""); err != nil {
			errs = append(errs, fmt.Errorf("install %q: %w", hs.Name, err))
			continue
		}
		names = append(names, hs.Name)
	}
	return names, errs
}

func (s *Service) Remove(name string) error {
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return fmt.Errorf("invalid skill name %q", name)
	}
	dir := filepath.Join(s.skillsDir, name)
	if _, err := os.Stat(filepath.Join(dir, skillFilename)); err != nil {
		return fmt.Errorf("skill %q is not installed", name)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove skill %q: %w", name, err)
	}
	return nil
}

// installDir validates a skill directory and copies it into the skills dir,
// staging next to the destination and renaming for atomicity. Reinstall
// overwrites.
func (s *Service) installDir(src string, nameOverride string) (string, error) {
	skill, err := skills.ParseSkillFile(filepath.Join(src, skillFilename), filepath.Base(src))
	if err != nil {
		return "", fmt.Errorf("invalid skill at %s: %w", src, err)
	}
	name := skill.Name
	if nameOverride != "" {
		name = nameOverride
	}
	if name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return "", fmt.Errorf("invalid skill name %q", name)
	}
	if err := os.MkdirAll(s.skillsDir, 0o755); err != nil {
		return "", fmt.Errorf("create skills dir: %w", err)
	}
	staging, err := os.MkdirTemp(s.skillsDir, ".install-"+name+"-*")
	if err != nil {
		return "", fmt.Errorf("stage skill: %w", err)
	}
	defer func() { _ = os.RemoveAll(staging) }()
	if err := os.CopyFS(staging, os.DirFS(src)); err != nil {
		return "", fmt.Errorf("copy skill: %w", err)
	}
	dst := filepath.Join(s.skillsDir, name)
	if err := os.RemoveAll(dst); err != nil {
		return "", fmt.Errorf("replace skill %q: %w", name, err)
	}
	if err := os.Rename(staging, dst); err != nil {
		return "", fmt.Errorf("place skill %q: %w", name, err)
	}
	return name, nil
}

func (s *Service) installURL(ctx context.Context, url string, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("--name is required when installing from a URL")
	}
	ctx, cancel := context.WithTimeout(ctx, urlFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("fetch skill: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch skill: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch skill: %s returned %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, urlFetchMaxBytes+1))
	if err != nil {
		return "", fmt.Errorf("fetch skill: %w", err)
	}
	if len(data) > urlFetchMaxBytes {
		return "", fmt.Errorf("fetch skill: %s exceeds %d bytes", url, urlFetchMaxBytes)
	}
	staging, err := os.MkdirTemp("", "invokr-skill-*")
	if err != nil {
		return "", fmt.Errorf("stage skill: %w", err)
	}
	defer func() { _ = os.RemoveAll(staging) }()
	dir := filepath.Join(staging, name)
	if err = os.MkdirAll(dir, 0o755); err == nil {
		err = os.WriteFile(filepath.Join(dir, skillFilename), data, 0o644)
	}
	if err != nil {
		return "", fmt.Errorf("stage skill: %w", err)
	}
	return s.installDir(dir, name)
}
