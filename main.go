package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/google/go-github/v56/github"
	"golang.org/x/oauth2"
)

const (
	refHistoryFile = ".ref-history"
)

type Config struct {
	GithubToken    string   // GitHub access token with repo permissions
	Owner          string   // Repository owner (user or organization)
	Repo           string   // Repository name
	TrunkBranch    string   // Base branch (typically main/master)
	TargetBranch   string   // Destination branch for merges
	RequiredLabels []string // Required PR labels to trigger merge
}

type RefHistory struct {
	Merges []MergeRecord `json:"merges"`
}

type MergeRecord struct {
	PR        int       `json:"pr"`
	Commit    string    `json:"commit"`
	Timestamp time.Time `json:"timestamp"`
}

func main() {
	ctx := context.Background()

	cfg, err := parseConfig()
	if err != nil {
		log.Fatal("Error:", err)
	}

	log.Printf("cfg: %+v\n", cfg)

	client := createGitHubClient(ctx, cfg)

	prs, err := fetchQualifiedPRs(ctx, client, cfg)
	if err != nil {
		log.Fatal("Error fetching PRs:", err)
	}

	log.Printf("prs filtered: %+v\n", prs)

	if err := recreateTargetBranch(cfg); err != nil {
		log.Fatal("Error preparing target branch:", err)
	}

	var mergedPRs []MergeRecord
	for _, pr := range prs {
		if err := processPR(pr); err != nil {
			log.Printf("Error procesando PR #%d: %v", pr.GetNumber(), err)
			continue
		}

		mergedPRs = append(mergedPRs, MergeRecord{
			PR:        pr.GetNumber(),
			Commit:    getLatestCommitSHA(),
			Timestamp: time.Now().UTC(),
		})
	}

	if err := updateRefHistory(mergedPRs); err != nil {
		log.Fatal("Error actualizando historial:", err)
	}

	if err := pushChanges(cfg); err != nil {
		log.Fatal("Error pushing changes:", err)
	}
}

// parseConfig initializes configuration from command line flags
func parseConfig() (Config, error) {
	var cfg Config
	var labels string

	flag.StringVar(&cfg.GithubToken, "github_token", "", "GitHub access token")
	flag.StringVar(&cfg.Owner, "owner", "", "Repository owner")
	flag.StringVar(&cfg.Repo, "repo", "", "Repository name")
	flag.StringVar(&cfg.TrunkBranch, "trunk_branch", "main", "Base branch")
	flag.StringVar(&cfg.TargetBranch, "target_branch", "", "Target branch")
	flag.StringVar(&labels, "labels", "", "Required PR labels")
	flag.Parse()

	if cfg.GithubToken == "" {
		return cfg, fmt.Errorf("github_token es requerido")
	}

	if cfg.Owner == "" {
		return cfg, fmt.Errorf("owner es requerido")
	}

	if cfg.Repo == "" {
		return cfg, fmt.Errorf("repo es requerido")
	}

	if cfg.TargetBranch == "" {
		cfg.TargetBranch = fmt.Sprintf("pre-%s", cfg.TrunkBranch)
	}

	cfg.RequiredLabels = parseLabels(labels)

	return cfg, nil
}

func parseLabels(input string) []string {
	if input == "" {
		return nil
	}

	labels := strings.Split(input, ",")
	cleaned := make([]string, 0, len(labels))

	for _, l := range labels {
		if trimmed := strings.TrimSpace(l); trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}

	return cleaned
}

// createGitHubClient initializes authenticated GitHub client
func createGitHubClient(ctx context.Context, cfg Config) *github.Client {
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: cfg.GithubToken},
	)
	return github.NewClient(oauth2.NewClient(ctx, ts))
}

func fetchQualifiedPRs(ctx context.Context, client *github.Client, cfg Config) ([]*github.PullRequest, error) {
	prs, _, err := client.PullRequests.List(ctx, cfg.Owner, cfg.Repo, &github.PullRequestListOptions{
		State: "open",
		Base:  cfg.TrunkBranch,
	})
	if err != nil {
		return nil, err
	}
	log.Printf("prs: %+v\n", prs)

	var filtered []*github.PullRequest
	for _, pr := range prs {
		if hasAnyLabel(pr.Labels, cfg.RequiredLabels) {
			filtered = append(filtered, pr)
		}
	}
	return filtered, nil
}

func hasAnyLabel(prLabels []*github.Label, required []string) bool {
	// Crear set de labels del PR en minúsculas
	prLabelsNormalized := make([]string, 0, len(prLabels))
	for _, l := range prLabels {
		prLabelsNormalized = append(prLabelsNormalized,
			strings.ToLower(strings.TrimSpace(l.GetName())))
	}

	log.Printf("prLabelsNormalized: %+v\n", prLabelsNormalized)
	log.Printf("required: %+v\n", required)

	// Verificar si ALGUNA label requerida existe en el PR
	for _, req := range required {
		reqNormalized := strings.ToLower(strings.TrimSpace(req))
		for _, prLabel := range prLabelsNormalized {
			if prLabel == reqNormalized {
				log.Printf("Match encontrado: PR Label '%s' == Required '%s'", prLabel, reqNormalized)
				return true
			}
		}
	}

	return false
}

func recreateTargetBranch(cfg Config) error {
	if err := exec.Command("git", "checkout", cfg.TrunkBranch).Run(); err != nil {
		return err
	}

	exec.Command("git", "branch", "-D", cfg.TargetBranch).Run()

	if err := exec.Command("git", "checkout", "-B", cfg.TargetBranch).Run(); err != nil {
		return err
	}
	return nil
}

func processPR(pr *github.PullRequest) error {
	// Fetch del PR
	if err := exec.Command("git", "fetch", "origin",
		fmt.Sprintf("pull/%d/head:pr-%d", pr.GetNumber(), pr.GetNumber())).Run(); err != nil {
		return fmt.Errorf("fetch failed: %v", err)
	}

	// Merge clásico estilo GitHub (--no-ff)
	cmd := exec.Command("git", "merge",
		"--no-ff",
		"-m",
		fmt.Sprintf("(#%d) %s", pr.GetNumber(), pr.GetTitle()),
	)

	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("merge failed: %s\n%s", err, output)
	}

	return nil
}

func updateRefHistory(processedPRs []MergeRecord) error {
	history := RefHistory{Merges: processedPRs}

	data, err := json.MarshalIndent(history, "", "  ")
	if err != nil {
		return fmt.Errorf("error marshaling history: %v", err)
	}

	if err := os.WriteFile(refHistoryFile, data, 0644); err != nil {
		return fmt.Errorf("error writing history file: %v", err)
	}

	// Stage y commit del archivo
	if err := exec.Command("git", "add", refHistoryFile).Run(); err != nil {
		return fmt.Errorf("error staging history file: %v", err)
	}

	return exec.Command("git", "commit", "-m", "chore: update ref-history").Run()
}

func pushChanges(cfg Config) error {
	return exec.Command("git", "push", "origin", cfg.TargetBranch, "--force").Run()
}

func getLatestCommitSHA() string {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	sha, _ := cmd.Output()
	return strings.TrimSpace(string(sha))
}
