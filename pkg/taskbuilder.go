// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"fmt"
	"strings"

	agentlib "github.com/bborbe/agent"
	"github.com/bborbe/agent/command/task"
)

// TaskConfig groups per-task envelope settings.
type TaskConfig struct {
	Stage string // "dev" or "prod" — emitted as the `stage` field
	// UpdateScope is the fleet-wide update scope emitted on every task
	// ("golang" | "deps"). Empty = unset — the field is omitted and the agent
	// defaults to "both", byte-identical to the pre-knob behaviour.
	UpdateScope string
}

// ComputeTaskTitle returns the frozen title form:
// "Update Go <owner>-<repo> <sha[:7]>".
//
// Dash, not slash-and-"at". CreateCommand.Validate rejects any '/' in a
// title, and SendCommand validates before publishing — a slash form would
// make every publish fail. The Stage-1 prototype's vault artifacts show a
// slash form in their frontmatter `title` field, but those artifacts were
// written directly by the prototype and never passed through this
// validator; the vault filenames it produced (which the production
// controller derives verbatim from `title`) already use the dash form,
// confirming the dash form is what the real contract requires.
func ComputeTaskTitle(c Candidate) string {
	return fmt.Sprintf(
		"Update Go %s-%s %s",
		c.Repo.Owner,
		c.Repo.Name,
		c.ShortSHA(),
	)
}

// BuildCreateCommand assembles the CreateTaskCommand for a Candidate.
func BuildCreateCommand(c Candidate, cfg TaskConfig) task.CreateCommand {
	taskIDStr := DeriveTaskID(c.Repo.Owner, c.Repo.Name, c.LatestGo.Number(), c.HeadSHA).String()
	return task.CreateCommand{
		Title:          ComputeTaskTitle(c),
		TaskIdentifier: agentlib.TaskIdentifier(taskIDStr),
		Frontmatter:    buildFrontmatter(c, taskIDStr, cfg),
		Body:           buildTaskBody(c),
	}
}

func buildFrontmatter(
	c Candidate,
	taskIDStr string,
	cfg TaskConfig,
) agentlib.TaskFrontmatter {
	return baseFrontmatter(c, ComputeTaskTitle(c), taskIDStr, cfg)
}

// ComputeMonthlyTaskTitle returns the frozen monthly title form:
// "Monthly Deps Update <owner>-<repo> <YYYY-MM>".
//
// Dash form for the same reason as ComputeTaskTitle: CreateCommand.Validate
// rejects any '/' in a title.
func ComputeMonthlyTaskTitle(c Candidate, month string) string {
	return fmt.Sprintf(
		"Monthly Deps Update %s-%s %s",
		c.Repo.Owner,
		c.Repo.Name,
		month,
	)
}

// BuildMonthlyCreateCommand assembles the monthly CreateTaskCommand for a
// Candidate. It carries the same envelope as the Go-version task; the month is
// carried by the title and by the month-keyed identifier, not by a frontmatter
// field — the task store rejects unknown frontmatter keys, so the month must
// ride in fields the store already accepts.
func BuildMonthlyCreateCommand(c Candidate, cfg TaskConfig, month string) task.CreateCommand {
	taskIDStr := DeriveMonthlyTaskID(c.Repo.Owner, c.Repo.Name, month).String()
	frontmatter := baseFrontmatter(c, ComputeMonthlyTaskTitle(c, month), taskIDStr, cfg)
	return task.CreateCommand{
		Title:          ComputeMonthlyTaskTitle(c, month),
		TaskIdentifier: agentlib.TaskIdentifier(taskIDStr),
		Frontmatter:    frontmatter,
		Body:           buildTaskBody(c),
	}
}

// baseFrontmatter builds the frozen task envelope shared by every create task.
// Only the title, the identifier and the body vary between task kinds.
func baseFrontmatter(
	c Candidate,
	title string,
	taskIDStr string,
	cfg TaskConfig,
) agentlib.TaskFrontmatter {
	frontmatter := agentlib.TaskFrontmatter{
		"task_type":       "github-update-go",
		"assignee":        "github-update-go-agent",
		"phase":           "planning",
		"status":          "in_progress",
		"stage":           cfg.Stage,
		"task_identifier": taskIDStr,
		"title":           title,
		"repo":            c.Repo.String(),
		"clone_url": fmt.Sprintf(
			"git@github.com:%s/%s.git",
			c.Repo.Owner,
			c.Repo.Name,
		),
		"ref":        c.HeadSHA,
		"current_go": c.CurrentGo.Number(),
		"latest_go":  c.LatestGo.Number(),
	}
	// Emit update_scope only when a fleet-wide scope is configured. Empty =
	// omitted, so the agent's "both" default applies — the emitted task is
	// byte-identical to the pre-knob shape.
	if cfg.UpdateScope != "" {
		frontmatter["update_scope"] = cfg.UpdateScope
	}
	return frontmatter
}

func buildTaskBody(c Candidate) string {
	owner := c.Repo.Owner
	name := c.Repo.Name
	return fmt.Sprintf(
		"# Update Go: %s/%s\n\n"+
			"**Current Go:** %s  ·  **Latest Go:** %s\n"+
			"**HEAD:** %s\n"+
			"**Repo:** [%s/%s](https://github.com/%s/%s)\n",
		owner, name,
		c.CurrentGo.Number(), c.LatestGo.Number(),
		c.ShortSHA(),
		owner, name, owner, name,
	)
}

// ComputeDecisionTaskTitle returns the frozen decision-task title form:
// "Go Update Decision <owner>-<repo>" — no HEAD SHA, matching the
// repo-keyed identity (spec 002 Desired Behavior 7).
func ComputeDecisionTaskTitle(c Candidate) string {
	return fmt.Sprintf("Go Update Decision %s-%s", c.Repo.Owner, c.Repo.Name)
}

// BuildDecisionCommand assembles the decision-task CreateTaskCommand for a
// Candidate whose consent is undecided (spec 002 Desired Behaviors 5-8, 10).
func BuildDecisionCommand(c Candidate, cfg TaskConfig) task.CreateCommand {
	taskIDStr := DeriveDecisionTaskID(c.Repo.Owner, c.Repo.Name).String()
	return task.CreateCommand{
		Title:          ComputeDecisionTaskTitle(c),
		TaskIdentifier: agentlib.TaskIdentifier(taskIDStr),
		Frontmatter:    buildDecisionFrontmatter(c, taskIDStr, cfg),
		Body:           buildDecisionTaskBody(c),
	}
}

func buildDecisionFrontmatter(
	c Candidate,
	taskIDStr string,
	cfg TaskConfig,
) agentlib.TaskFrontmatter {
	return agentlib.TaskFrontmatter{
		"task_type":       "github-update-go-decision",
		"assignee":        "bborbe",
		"phase":           "human_review",
		"status":          "in_progress",
		"stage":           cfg.Stage,
		"task_identifier": taskIDStr,
		"title":           ComputeDecisionTaskTitle(c),
		"repo":            c.Repo.String(),
		"current_go":      c.CurrentGo.Number(),
		"latest_go":       c.LatestGo.Number(),
	}
}

// StaleIgnoreFinding is one repo's stale-ignore verdict: the advisory IDs OSV
// reports as withdrawn, and per ID the ignore surface files that declare it.
type StaleIgnoreFinding struct {
	IDs      []string            // sorted, de-duplicated
	Surfaces map[string][]string // advisory ID -> surface paths, in IgnoreSurfaceFiles order
}

// ComputeStaleIgnoreTaskTitle returns the frozen title form
// "Remove Stale Ignores <owner>-<repo>" — dash form, no HEAD SHA: the identity
// is the stale set, not the commit. CreateCommand.Validate rejects any '/' in a
// title, so the dash form is mandatory.
func ComputeStaleIgnoreTaskTitle(c Candidate) string {
	return fmt.Sprintf("Remove Stale Ignores %s-%s", c.Repo.Owner, c.Repo.Name)
}

// BuildStaleIgnoreCommand assembles the CreateTaskCommand for one stale-ignore
// finding. The envelope is exactly BuildCreateCommand's — same task_type,
// assignee, phase, status, stage, repo, clone_url, ref, current_go, latest_go
// and optional update_scope — with only the identifier, the title and the body
// different. target_vault is NOT set here: it is stamped at send time by the
// typed sender.
func BuildStaleIgnoreCommand(
	c Candidate,
	finding StaleIgnoreFinding,
	generation int,
	cfg TaskConfig,
) task.CreateCommand {
	title := ComputeStaleIgnoreTaskTitle(c)
	taskIDStr := DeriveStaleIgnoreTaskID(
		c.Repo.Owner,
		c.Repo.Name,
		generation,
		finding.IDs,
	).String()
	return task.CreateCommand{
		Title:          title,
		TaskIdentifier: agentlib.TaskIdentifier(taskIDStr),
		Frontmatter:    baseFrontmatter(c, title, taskIDStr, cfg),
		Body:           buildStaleIgnoreBody(c, finding),
	}
}

func buildStaleIgnoreBody(c Candidate, finding StaleIgnoreFinding) string {
	owner := c.Repo.Owner
	name := c.Repo.Name
	var body strings.Builder
	fmt.Fprintf(&body, "# Remove Stale Ignores: %s/%s\n\n", owner, name)
	body.WriteString(
		"OSV reports the following advisories as withdrawn, but this repo still\n" +
			"suppresses them:\n\n",
	)
	for _, id := range finding.IDs {
		files := finding.Surfaces[id]
		quoted := make([]string, 0, len(files))
		for _, file := range files {
			quoted = append(quoted, "`"+file+"`")
		}
		fmt.Fprintf(&body, "- `%s` — surfaces: %s\n", id, strings.Join(quoted, ", "))
	}
	fmt.Fprintf(
		&body,
		"\n**Repo:** [%s/%s](https://github.com/%s/%s)\n",
		owner, name, owner, name,
	)
	fmt.Fprintf(&body, "**HEAD:** %s\n", c.ShortSHA())
	return body.String()
}

func buildDecisionTaskBody(c Candidate) string {
	owner := c.Repo.Owner
	name := c.Repo.Name
	return fmt.Sprintf(
		"# Go Update Decision Needed: %s/%s\n\n"+
			"This repo's declared Go version is behind the latest stable release, "+
			"and nobody has recorded whether it should be updated automatically.\n\n"+
			"**Current Go:** %s  ·  **Latest Go:** %s\n"+
			"**Repo:** [%s/%s](https://github.com/%s/%s)\n\n"+
			"Add one of the following to `.maintainer.yaml` to answer:\n\n"+
			"```yaml\n"+
			"goUpdate:\n"+
			"  autoUpdate: true   # opt in — this repo starts receiving automatic Go bump PRs\n"+
			"```\n\n"+
			"or\n\n"+
			"```yaml\n"+
			"goUpdate:\n"+
			"  autoUpdate: false  # opt out — this repo stays silent going forward\n"+
			"```\n\n"+
			"Either answer makes this repo silent again; only an unanswered decision "+
			"re-files this task on the next scan.\n",
		owner, name,
		c.CurrentGo.Number(), c.LatestGo.Number(),
		owner, name, owner, name,
	)
}
