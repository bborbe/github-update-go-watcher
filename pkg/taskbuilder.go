// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"fmt"

	agentlib "github.com/bborbe/agent"
	"github.com/bborbe/agent/command/task"
)

// TaskConfig groups per-task envelope settings.
type TaskConfig struct {
	Stage string // "dev" or "prod" — emitted as the `stage` field
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
	taskIDStr := DeriveTaskID(c.Repo.Owner, c.Repo.Name, c.HeadSHA).String()
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
	return agentlib.TaskFrontmatter{
		"task_type":       "github-update-go",
		"assignee":        "github-update-go-agent",
		"phase":           "planning",
		"status":          "in_progress",
		"stage":           cfg.Stage,
		"task_identifier": taskIDStr,
		"title":           ComputeTaskTitle(c),
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
