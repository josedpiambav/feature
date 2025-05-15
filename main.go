package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/go-github/v56/github"
	"golang.org/x/oauth2"
)

const (
	refHistoryFile = ".ref-history"
)

type Config struct {
	GithubToken    string
	Owner          string
	Repo           string
	TrunkBranch    string
	TargetBranch   string
	RequiredLabels []string
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

	client := createGitHubClient(ctx, cfg)

	if err := ensureTargetBranch(ctx, client, cfg); err != nil {
		log.Fatal("Branch setup error:", err)
	}

	prs, err := getQualifiedPRs(ctx, client, cfg)
	if err != nil {
		log.Fatal("PR fetch error:", err)
	}

	var mergedPRs []MergeRecord
	for _, pr := range prs {
		commitSHA, err := processPR(ctx, client, cfg, pr)
		if err != nil {
			log.Printf("Error processing PR #%d: %v", pr.GetNumber(), err)
			continue
		}

		mergedPRs = append(mergedPRs, MergeRecord{
			PR:        pr.GetNumber(),
			Commit:    commitSHA,
			Timestamp: time.Now().UTC(),
		})
	}

	if err := updateRefHistory(ctx, client, cfg, mergedPRs); err != nil {
		log.Fatal("History update error:", err)
	}
}

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
		return cfg, fmt.Errorf("github_token is required")
	}

	if cfg.Owner == "" || cfg.Repo == "" {
		return cfg, fmt.Errorf("owner and repo are required")
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
	return strings.Split(input, ",")
}

func createGitHubClient(ctx context.Context, cfg Config) *github.Client {
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: cfg.GithubToken},
	)
	return github.NewClient(oauth2.NewClient(ctx, ts))
}

func ensureTargetBranch(ctx context.Context, client *github.Client, cfg Config) error {
	_, resp, err := client.Repositories.GetBranch(ctx, cfg.Owner, cfg.Repo, cfg.TargetBranch, 0)
	if resp.StatusCode == 404 {
		baseRef, _, err := client.Repositories.GetBranch(ctx, cfg.Owner, cfg.Repo, cfg.TrunkBranch, 0)
		if err != nil {
			return fmt.Errorf("error getting base branch: %v", err)
		}

		_, _, err = client.Git.CreateRef(ctx, cfg.Owner, cfg.Repo, &github.Reference{
			Ref:    github.String("refs/heads/" + cfg.TargetBranch),
			Object: &github.GitObject{SHA: baseRef.Commit.SHA},
		})
		return err
	}
	return err
}

func getQualifiedPRs(ctx context.Context, client *github.Client, cfg Config) ([]*github.PullRequest, error) {
	prs, _, err := client.PullRequests.List(ctx, cfg.Owner, cfg.Repo, &github.PullRequestListOptions{
		State: "open",
		Base:  cfg.TrunkBranch,
	})
	if err != nil {
		return nil, fmt.Errorf("error listing PRs: %v", err)
	}

	return filterPRs(prs, cfg.RequiredLabels), nil
}

func filterPRs(prs []*github.PullRequest, requiredLabels []string) []*github.PullRequest {
	var filtered []*github.PullRequest
	for _, pr := range prs {
		if hasAnyLabel(pr.Labels, requiredLabels) {
			filtered = append(filtered, pr)
		}
	}
	return filtered
}

func hasAnyLabel(prLabels []*github.Label, required []string) bool {
	requiredSet := make(map[string]struct{})
	for _, label := range required {
		requiredSet[strings.ToLower(strings.TrimSpace(label))] = struct{}{}
	}

	for _, prLabel := range prLabels {
		labelName := strings.ToLower(strings.TrimSpace(prLabel.GetName()))
		if _, exists := requiredSet[labelName]; exists {
			return true
		}
	}
	return false
}

func processPR(ctx context.Context, client *github.Client, cfg Config, pr *github.PullRequest) (string, error) {
	// Obtener el último commit de la rama target
	baseRepoCommit, _, err := client.Repositories.GetCommit(ctx, cfg.Owner, cfg.Repo, cfg.TargetBranch, nil)
	if err != nil {
		return "", fmt.Errorf("error getting base commit: %v", err)
	}

	if baseRepoCommit.Commit == nil {
		return "", fmt.Errorf("base commit is nil")
	}
	baseCommit := baseRepoCommit.Commit

	prCommits, _, err := client.PullRequests.ListCommits(ctx, cfg.Owner, cfg.Repo, pr.GetNumber(), nil)
	if err != nil {
		return "", fmt.Errorf("error getting PR commits: %v", err)
	}

	// Crear tree entries
	var treeEntries []*github.TreeEntry
	for _, prCommit := range prCommits {
		commit, _, err := client.Git.GetCommit(ctx, cfg.Owner, cfg.Repo, prCommit.GetSHA())
		if err != nil {
			return "", fmt.Errorf("error getting commit details: %v", err)
		}

		treeEntries = append(treeEntries, &github.TreeEntry{
			Path:    github.String(fmt.Sprintf("pr-%d/%s", pr.GetNumber(), prCommit.GetSHA())),
			Mode:    github.String("100644"),
			Type:    github.String("blob"),
			Content: github.String("Squashed changes"),
			SHA:     commit.Tree.SHA,
		})
	}

	tree, _, err := client.Git.CreateTree(
		ctx,
		cfg.Owner,
		cfg.Repo,
		*baseCommit.SHA,
		treeEntries,
	)
	if err != nil {
		return "", fmt.Errorf("error creating tree: %v", err)
	}

	// Crear commit con los padres correctos
	newCommit, _, err := client.Git.CreateCommit(
		ctx,
		cfg.Owner,
		cfg.Repo,
		&github.Commit{
			Message: github.String(pr.GetTitle()),
			Tree:    tree,
			Parents: []*github.Commit{baseCommit}, // Ahora usando el tipo correcto
		},
		nil,
	)
	if err != nil {
		return "", fmt.Errorf("error creating commit: %v", err)
	}

	_, _, err = client.Git.UpdateRef(
		ctx,
		cfg.Owner,
		cfg.Repo,
		&github.Reference{
			Ref:    github.String("refs/heads/" + cfg.TargetBranch),
			Object: &github.GitObject{SHA: newCommit.SHA},
		},
		true,
	)

	return newCommit.GetSHA(), err
}

func updateRefHistory(ctx context.Context, client *github.Client, cfg Config, records []MergeRecord) error {
	content, err := json.MarshalIndent(RefHistory{Merges: records}, "", "  ")
	if err != nil {
		return fmt.Errorf("error marshaling history: %v", err)
	}

	_, _, err = client.Repositories.CreateFile(ctx, cfg.Owner, cfg.Repo, refHistoryFile, &github.RepositoryContentFileOptions{
		Message: github.String("chore: add .ref-history"),
		Content: content,
		Branch:  github.String(cfg.TargetBranch),
		Committer: &github.CommitAuthor{
			Name:  github.String("GitHub Actions"),
			Email: github.String("actions@github.com"),
		},
	})
	return err
}
