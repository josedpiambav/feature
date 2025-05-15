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
			log.Printf("Error processing PR #%d: %v", pr.GetNumber(), err)
			continue
		}
		mergedPRs = append(mergedPRs, MergeRecord{
			PR:        pr.GetNumber(),
			Commit:    pr.GetHead().GetSHA(),
			Timestamp: time.Now().UTC(),
		})
	}

	if err := updateRefHistory(mergedPRs); err != nil {
		log.Fatal("Error updating ref history:", err)
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
		if hasAllLabels(pr.Labels, cfg.RequiredLabels) && isBranchExists(pr.GetHead().GetRef()) {
			filtered = append(filtered, pr)
		}
	}
	return filtered, nil
}

func hasAllLabels(prLabels []*github.Label, required []string) bool {
	labelSet := make(map[string]struct{})
	for _, l := range prLabels {
		labelSet[strings.ToLower(l.GetName())] = struct{}{}
	}

	for _, req := range required {
		if _, exists := labelSet[strings.ToLower(req)]; !exists {
			return false
		}
	}
	return true
}

func isBranchExists(branch string) bool {
	cmd := exec.Command("git", "show-ref", "--verify", fmt.Sprintf("refs/heads/%s", branch))
	return cmd.Run() == nil
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
	// Fetch PR como branch temporal
	if err := exec.Command("git", "fetch", "origin",
		fmt.Sprintf("pull/%d/head:pr-%d", pr.GetNumber(), pr.GetNumber())).Run(); err != nil {
		return err
	}

	// Merge explícito con --no-ff
	cmd := exec.Command("git", "merge",
		fmt.Sprintf("pr-%d", pr.GetNumber()),
		"--no-ff",
		"-m",
		fmt.Sprintf("(#%d) %s", pr.GetNumber(), pr.GetTitle()))

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("merge failed: %v", err)
	}

	return nil
}

func updateRefHistory(merges []MergeRecord) error {
	history := RefHistory{Merges: merges}
	data, err := json.MarshalIndent(history, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile(refHistoryFile, data, 0644); err != nil {
		return err
	}

	cmd := exec.Command("git", "add", refHistoryFile)
	if err := cmd.Run(); err != nil {
		return err
	}

	return exec.Command("git", "commit", "-m", "chore: update ref history").Run()
}

func pushChanges(cfg Config) error {
	return exec.Command("git", "push", "origin", cfg.TargetBranch, "--force").Run()
}
