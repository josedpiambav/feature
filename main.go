package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	refHistoryFile = ".ref-history"
	githubAPI      = "https://api.github.com"
	userAgent      = "GitHubMergeBot/1.0"
)

type Config struct {
	GithubToken    string   `json:"github_token"`
	Owner          string   `json:"owner"`
	Repo           string   `json:"repo"`
	TrunkBranch    string   `json:"trunk_branch"`
	TargetBranch   string   `json:"target_branch"`
	RequiredLabels []string `json:"required_labels"`
}

type RefHistory struct {
	Merges []MergeRecord `json:"merges"`
}

type MergeRecord struct {
	PR        int       `json:"pr"`
	Commit    string    `json:"commit"`
	Timestamp time.Time `json:"timestamp"`
}

type GitHubPR struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	Base   struct {
		Ref string `json:"ref"`
	} `json:"base"`
	Labels []string `json:"labels"`
}

func main() {
	cfg, err := parseConfig()
	if err != nil {
		log.Fatal("Configuración inválida:", err)
	}

	if err := setupGitConfig(); err != nil {
		log.Fatal("Error configurando Git:", err)
	}

	prs, err := fetchQualifiedPRs(cfg)
	if err != nil {
		log.Fatal("Error obteniendo PRs:", err)
	}

	if err := recreateTargetBranch(cfg); err != nil {
		log.Fatal("Error preparando rama destino:", err)
	}

	var mergedPRs []MergeRecord
	for _, pr := range prs {
		if err := processPR(pr); err != nil {
			log.Printf("PR #%d falló: %v", pr.Number, err)
			continue
		}

		mergedPRs = append(mergedPRs, createMergeRecord(pr))
	}

	if err := updateRefHistory(mergedPRs); err != nil {
		log.Fatal("Error actualizando historial:", err)
	}

	if err := pushChanges(cfg); err != nil {
		log.Fatal("Error subiendo cambios:", err)
	}
}

func parseConfig() (Config, error) {
	var cfg Config
	var labels string

	flag.StringVar(&cfg.GithubToken, "github_token", "", "GitHub access token")
	flag.StringVar(&cfg.Owner, "owner", "", "Repository owner")
	flag.StringVar(&cfg.Repo, "repo", "", "Repository name")
	flag.StringVar(&cfg.TrunkBranch, "trunk_branch", "main", "Base branch name")
	flag.StringVar(&cfg.TargetBranch, "target_branch", "", "Target branch name")
	flag.StringVar(&labels, "labels", "", "PR labels")
	flag.Parse()

	if cfg.GithubToken == "" || cfg.Owner == "" || cfg.Repo == "" || labels == "" {
		return cfg, fmt.Errorf("faltan parámetros requeridos")
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

func setupGitConfig() error {
	configs := map[string]string{
		"safe.directory":        "/github/workspace",
		"user.name":             "github-actions[bot]",
		"user.email":            "41898282+github-actions[bot]@users.noreply.github.com",
		"advice.addIgnoredFile": "false",
	}

	for key, value := range configs {
		cmd := exec.Command("git", "config", "--global", key, value)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git config error: %s\n%s", err, output)
		}
	}
	return nil
}

func fetchQualifiedPRs(cfg Config) ([]GitHubPR, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls?state=open&base=%s",
		githubAPI, cfg.Owner, cfg.Repo, cfg.TrunkBranch)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("error creando request: %w", err)
	}

	req.Header.Set("Authorization", "token "+cfg.GithubToken)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", userAgent)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error realizando solicitud: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API respondió con código %d", resp.StatusCode)
	}

	var rawPRs []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		State  string `json:"state"`
		Base   struct {
			Ref string `json:"ref"`
		} `json:"base"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&rawPRs); err != nil {
		return nil, fmt.Errorf("error decodificando respuesta: %w", err)
	}

	prs := make([]GitHubPR, len(rawPRs))
	for i, raw := range rawPRs {
		labels := make([]string, len(raw.Labels))
		for j, l := range raw.Labels {
			labels[j] = l.Name
		}

		prs[i] = GitHubPR{
			Number: raw.Number,
			Title:  raw.Title,
			State:  raw.State,
			Base:   raw.Base,
			Labels: labels,
		}
	}

	return filterPRs(prs, cfg.RequiredLabels), nil
}

func filterPRs(prs []GitHubPR, requiredLabels []string) []GitHubPR {
	var filtered []GitHubPR
	for _, pr := range prs {
		if hasAnyLabel(pr.Labels, requiredLabels) {
			filtered = append(filtered, pr)
		}
	}
	return filtered
}

func hasAnyLabel(prLabels []string, required []string) bool {
	prLabelSet := make(map[string]struct{})
	for _, l := range prLabels {
		prLabelSet[strings.ToLower(l)] = struct{}{}
	}

	for _, req := range required {
		if _, exists := prLabelSet[strings.ToLower(req)]; exists {
			return true
		}
	}
	return false
}

func recreateTargetBranch(cfg Config) error {
	if err := runGitCommand("checkout", cfg.TrunkBranch); err != nil {
		return fmt.Errorf("error cambiando a trunk branch: %w", err)
	}

	if branchExists(cfg.TargetBranch) {
		if err := runGitCommand("branch", "-D", cfg.TargetBranch); err != nil {
			return fmt.Errorf("error eliminando rama target: %w", err)
		}
	}

	if err := runGitCommand("checkout", "-B", cfg.TargetBranch); err != nil {
		return fmt.Errorf("error creando rama target: %w", err)
	}

	return nil
}

func branchExists(branch string) bool {
	cmd := exec.Command("git", "show-ref", "--verify", fmt.Sprintf("refs/heads/%s", branch))
	return cmd.Run() == nil
}

func runGitCommand(args ...string) error {
	cmd := exec.Command("git", args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("error ejecutando 'git %s': %s\n%s",
			strings.Join(args, " "), err, string(output))
	}
	return nil
}

func processPR(pr GitHubPR) error {
	branch := fmt.Sprintf("pr-%d", pr.Number)

	if err := fetchPRBranch(pr.Number, branch); err != nil {
		return err
	}

	if err := performSquashMerge(branch, pr.Title); err != nil {
		return err
	}

	return nil
}

func fetchPRBranch(prNumber int, branch string) error {
	cmd := exec.Command("git", "fetch", "origin",
		fmt.Sprintf("pull/%d/head:%s", prNumber, branch))

	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("error fetching PR: %s\n%s", err, output)
	}
	return nil
}

func performSquashMerge(branch, message string) error {
	mergeCmd := exec.Command("git", "merge", "--squash", branch)
	if output, err := mergeCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("squash merge fallido: %s\n%s", err, output)
	}

	commitCmd := exec.Command("git", "commit", "-m", message)
	if output, err := commitCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("commit fallido: %s\n%s", err, output)
	}
	return nil
}

func updateRefHistory(merges []MergeRecord) error {
	history := RefHistory{Merges: merges}
	data, err := json.MarshalIndent(history, "", "  ")
	if err != nil {
		return fmt.Errorf("error serializando historial: %w", err)
	}

	if err := os.WriteFile(refHistoryFile, data, 0644); err != nil {
		return fmt.Errorf("error escribiendo archivo: %w", err)
	}

	if err := stageAndCommitHistory(); err != nil {
		return fmt.Errorf("error confirmando cambios: %w", err)
	}
	return nil
}

func stageAndCommitHistory() error {
	addCmd := exec.Command("git", "add", refHistoryFile)
	if output, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("error staging: %s\n%s", err, output)
	}

	commitCmd := exec.Command("git", "commit", "-m", "chore: update ref-history")
	if output, err := commitCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("error commit: %s\n%s", err, output)
	}
	return nil
}

func pushChanges(cfg Config) error {
	cmd := exec.Command("git", "push", "origin", cfg.TargetBranch, "--force")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("push fallido: %s\n%s", err, output)
	}
	return nil
}

func createMergeRecord(pr GitHubPR) MergeRecord {
	return MergeRecord{
		PR:        pr.Number,
		Commit:    getLatestCommitSHA(),
		Timestamp: time.Now().UTC(),
	}
}

func getLatestCommitSHA() string {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(output))
}
