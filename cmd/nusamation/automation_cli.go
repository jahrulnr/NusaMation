package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/jahrulnr/nusamation/internal/store"
)

const automationUsage = `usage: nusamation automation <list|get|add|set|pause|resume|remove|history> [flags]

  list [-json]                         List jobs
  get <id> [-json]                     Show a job and its history
  add -name NAME -prompt TEXT -agent SLUG (-at TIME | -cron 'M H D M W') [-session isolated|same] [-tz ZONE] [-expires TIME]
  set <id> [same flags as add]          Update selected fields
  pause|resume|remove|history <id>       Manage a job

TIME is RFC3339, or YYYY-MM-DDTHH:MM in the selected timezone. CLI changes are picked up by the running server.
All commands accept -data-dir DIR; list/get/history also accept -json.
`

func automationCmd(args []string) error {
	if len(args) == 0 {
		return usagef("missing automation subcommand")
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Print(automationUsage)
		return flag.ErrHelp
	case "add", "create", "set", "update":
		return automationSave(args[0], args[1:])
	case "list", "ls", "get", "show", "pause", "resume", "remove", "delete", "history":
		return automationManage(args[0], args[1:])
	default:
		return usagef("unknown automation subcommand %q", args[0])
	}
}
func automationTime(v, zone string) (int64, error) {
	if v == "" {
		return 0, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.UnixMilli(), nil
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return 0, err
	}
	t, err := time.ParseInLocation("2006-01-02T15:04", v, loc)
	if err != nil {
		return 0, fmt.Errorf("invalid time %q: use RFC3339 or YYYY-MM-DDTHH:MM", v)
	}
	return t.UnixMilli(), nil
}
func automationSave(action string, args []string) error {
	fs, dataDir := subFlagSet("automation "+action, automationUsage)
	name := fs.String("name", "", "name")
	prompt := fs.String("prompt", "", "prompt")
	agentID := fs.String("agent", "", "agent slug or id")
	cronExpr := fs.String("cron", "", "five-field cron")
	at := fs.String("at", "", "one-time date and time")
	zone := fs.String("tz", "", "IANA timezone")
	expires := fs.String("expires", "", "expiry date and time; empty clears")
	mode := fs.String("session", "", "isolated or same")
	jsonOut := fs.Bool("json", false, "JSON output")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	edit := action == "set" || action == "update"
	if (edit && len(pos) != 1) || (!edit && len(pos) != 0) {
		return usagef("add takes no id; set requires one id")
	}
	present := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { present[f.Name] = true })
	st, err := openStore(*dataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	var job store.Automation
	if edit {
		job, err = st.GetAutomation(ctx, pos[0])
		if err != nil {
			return err
		}
	} else {
		job.Enabled = true
		job.SessionMode = "isolated"
		job.Timezone = store.LocalTimezone()
	}
	if present["name"] {
		job.Name = *name
	}
	if present["prompt"] {
		job.Prompt = *prompt
	}
	if present["session"] {
		job.SessionMode = *mode
	}
	if present["tz"] {
		job.Timezone = *zone
	}
	if present["agent"] {
		a, e := resolveAgent(ctx, st, *agentID)
		if e != nil {
			return e
		}
		job.AgentID = a.ID
	}
	if present["cron"] && present["at"] {
		return usagef("choose either -cron or -at")
	}
	if present["cron"] {
		job.Schedule = "cron"
		job.Cron = *cronExpr
		job.RunAt = 0
	}
	if present["at"] {
		job.Schedule = "once"
		job.RunAt, err = automationTime(*at, job.Timezone)
		if err != nil {
			return err
		}
		job.Cron = ""
	}
	if present["expires"] {
		job.ExpiresAt, err = automationTime(*expires, job.Timezone)
		if err != nil {
			return err
		}
	}
	job, err = st.SaveAutomation(ctx, job, time.Now())
	if err != nil {
		return err
	}
	if *jsonOut {
		printJSON(job)
	} else {
		fmt.Printf("%s\t%s\n", job.ID, job.Name)
	}
	return nil
}
func automationManage(action string, args []string) error {
	fs, dataDir := subFlagSet("automation "+action, automationUsage)
	jsonOut := fs.Bool("json", false, "JSON output")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	list := action == "list" || action == "ls"
	if (list && len(pos) != 0) || (!list && len(pos) != 1) {
		return usagef("list takes no id; this command requires one id")
	}
	st, err := openStore(*dataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	if list {
		jobs, err := st.ListAutomations(ctx)
		if err != nil {
			return err
		}
		if *jsonOut {
			printJSON(jobs)
		} else {
			for _, j := range jobs {
				fmt.Printf("%s\t%s\t%s\tenabled=%t\tnext=%s\n", j.ID, j.Name, j.Schedule, j.Enabled, formatAutomationTime(j.NextRunAt))
			}
		}
		return nil
	}
	id := pos[0]
	switch action {
	case "pause", "resume":
		return st.SetAutomationEnabled(ctx, id, action == "resume")
	case "remove", "delete":
		return st.DeleteAutomation(ctx, id)
	case "history":
		if _, err = st.GetAutomation(ctx, id); err != nil {
			return err
		}
		runs, err := st.ListAutomationRuns(ctx, id)
		if err != nil {
			return err
		}
		if *jsonOut {
			printJSON(runs)
		} else {
			for _, r := range runs {
				fmt.Printf("%s\t%s\t%s\t%s\t%s\n", r.ID, formatAutomationTime(r.StartedAt), r.Status, r.ConversationID, r.Error)
			}
		}
		return nil
	default:
		j, err := st.GetAutomation(ctx, id)
		if err != nil {
			return err
		}
		runs, err := st.ListAutomationRuns(ctx, id)
		if err != nil {
			return err
		}
		if *jsonOut {
			printJSON(map[string]any{"automation": j, "runs": runs})
		} else {
			fmt.Printf("id: %s\nname: %s\nagent: %s\nprompt: %s\nschedule: %s %s\nsession: %s\ntimezone: %s\nnext: %s\nexpiry: %s\nenabled: %t\nruns: %d\n", j.ID, j.Name, j.AgentID, j.Prompt, j.Schedule, j.Cron, j.SessionMode, j.Timezone, formatAutomationTime(j.NextRunAt), formatAutomationTime(j.ExpiresAt), j.Enabled, len(runs))
		}
		return nil
	}
}
func formatAutomationTime(v int64) string {
	if v == 0 {
		return "-"
	}
	return time.UnixMilli(v).Format(time.RFC3339)
}
