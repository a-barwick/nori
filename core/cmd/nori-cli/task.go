package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"nori/internal/permissions"
	"nori/internal/state"
)

type taskSummary struct {
	ID       string   `json:"task_id"`
	Title    string   `json:"title"`
	Status   string   `json:"status"`
	Bucket   string   `json:"bucket"`
	Progress string   `json:"progress,omitempty"`
	Results  []string `json:"result_ids"`
}

type taskDetail struct {
	Item         taskSummary               `json:"item"`
	Interactions []permissions.Interaction `json:"interactions"`
	Result       string                    `json:"result"`
}

func (a *cli) tasks(ctx context.Context, args []string) error {
	switch len(args) {
	case 0:
		var payload struct {
			Items []taskSummary `json:"items"`
		}
		if err := a.client.call(ctx, "GET", "/v1/work", nil, &payload); err != nil {
			return err
		}
		if a.machine {
			return a.emit(payload.Items)
		}
		for _, task := range payload.Items {
			fmt.Fprintf(a.out, "%s\t%s\t%s\n", task.ID, task.Bucket, task.Title)
		}
		return nil
	case 1:
		var data json.RawMessage
		if err := a.client.call(ctx, "GET", "/v1/work/"+url.PathEscape(args[0]), nil, &data); err != nil {
			return err
		}
		if a.machine {
			return a.emit(data)
		}
		var detail taskDetail
		if err := json.Unmarshal(data, &detail); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "%s  %s\n%s\n", detail.Item.ID, detail.Item.Status, detail.Item.Title)
		if detail.Item.Progress != "" {
			fmt.Fprintln(a.out, detail.Item.Progress)
		}
		for _, question := range detail.Interactions {
			if question.Status == "pending" {
				fmt.Fprintf(a.out, "%s %s  %s\n", question.Kind, question.ID, question.Prompt)
			}
		}
		for _, result := range detail.Item.Results {
			fmt.Fprintf(a.out, "Result %s\n", result)
		}
		if detail.Result != "" {
			fmt.Fprintln(a.out, detail.Result)
		}
		return nil
	default:
		return errors.New("tasks takes at most one task ID")
	}
}

type change struct {
	RequestID string `json:"request_id"`
	Revision  int64  `json:"expected_revision"`
	Value     any    `json:"value"`
}

func (a *cli) task(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("use task start, stop, answer, approve or decline")
	}
	action := args[0]
	flags := a.flags("task " + action)
	explicit := flags.String("request-id", "", "stable request UUID")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	values := flags.Args()
	switch action {
	case "start":
		if len(values) == 0 {
			return errors.New("task start requires a request")
		}
	case "answer":
		if len(values) < 2 {
			return errors.New("task answer requires an interaction ID and answer")
		}
	case "stop", "approve", "decline":
		if len(values) != 1 {
			return fmt.Errorf("task %s requires one ID", action)
		}
	default:
		return fmt.Errorf("unknown task command %q", action)
	}
	id, done, err := journalID(*explicit, a.scope(), append([]string{"task", action}, values...))
	if err != nil {
		return err
	}
	var receipt state.MutationReceipt
	switch action {
	case "start":
		err = a.client.call(ctx, "POST", "/v1/work", map[string]string{"request_id": id, "request": strings.Join(values, " ")}, &receipt)
	case "stop":
		path := "/v1/work/" + url.PathEscape(values[0]) + "/actions"
		err = a.client.call(ctx, "POST", path, change{id, 0, map[string]string{"action": "stop"}}, &receipt)
	default:
		err = a.respond(ctx, id, action, values, &receipt)
	}
	if err != nil {
		return fmt.Errorf("%w (request %s)", err, id)
	}
	if a.machine {
		err = a.emit(receipt)
	} else if receipt.Work != nil {
		_, err = fmt.Fprintln(a.out, receipt.Work.TaskID)
	} else {
		_, err = fmt.Fprintln(a.out, receipt.Status)
	}
	if err == nil {
		done()
	}
	return err
}

func (a *cli) respond(ctx context.Context, id, action string, values []string, receipt *state.MutationReceipt) error {
	path := "/v1/interactions/" + url.PathEscape(values[0])
	var interaction permissions.Interaction
	err := a.client.call(ctx, "GET", path, nil, &interaction)
	if err != nil && !isStatus(err, http.StatusNotFound) {
		return err
	}
	if interaction.Status != "pending" {
		if err := a.client.call(ctx, "GET", "/v1/mutations/"+id, nil, receipt); err == nil {
			return nil
		}
		return errors.New("interaction is no longer pending")
	}
	response := permissions.Response{Kind: action}
	if action == "answer" {
		response.Text = strings.Join(values[1:], " ")
	} else {
		response.ActionDigest = interaction.ActionDigest
	}
	return a.client.call(ctx, "POST", path+"/responses", change{id, interaction.Revision, response}, receipt)
}
