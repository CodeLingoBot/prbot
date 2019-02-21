package main

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/github"
	"github.com/juju/errors"
	"golang.org/x/oauth2"
	git "gopkg.in/src-d/go-git.v4"
	"gopkg.in/src-d/go-git.v4/plumbing"
	"gopkg.in/src-d/go-git.v4/storage/memory"

	slack "github.com/ashwanthkumar/slack-go-webhook"
)

func main() {
	fmt.Println("Listening for Pull Request activity...")
	err := forPRChange(context.Background(), os.Args[1], os.Args[2])
	log.Fatal(err)
}

func getNotifications(client *github.Client, since, before time.Time) ([]*github.Notification, error) {
	opts := &github.NotificationListOptions{
		All:    true,
		Since:  since,
		Before: before,
	}

	var notifications []*github.Notification
	notifications, _, err := client.Activity.ListNotifications(context.Background(), opts)
	if err != nil {
		return nil, errors.Trace(err)
	}

	return notifications, nil
}

func notifySlack(ctx context.Context, client *github.Client, not github.Notification) error {
	// I don't know why this is returning the PRURL
	parsed, err := url.Parse(*not.Subject.LatestCommentURL)
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	parts[3] = "pull"

	owner := parts[1]
	repo := parts[2]
	num, err := strconv.Atoi(parts[4])
	if err != nil {
		return errors.Trace(err)
	}

	pr, _, err := client.PullRequests.Get(ctx, owner, repo, num)
	if err != nil {
		return errors.Trace(err)
	}

	var status string
	if *pr.Merged {
		status = "Merged"
	} else if pr.ClosedAt == nil {
		status = "Open"
	} else {
		status = "Closed"
	}

	webhookURL := os.Args[3]

	attachment := slack.Attachment{}
	attachment.AddField(slack.Field{Title: "Repository", Value: owner + "/" + repo})
	attachment.AddField(slack.Field{Title: "Status", Value: status})
	// Note: red button is Style: "danger"
	attachment.AddAction(slack.Action{Type: "button", Text: "View Pull Request", Url: "https://github.com/" + strings.Join(parts[1:], "/"), Style: "primary"})
	if status == "Merged" {
		attachment.AddAction(slack.Action{Type: "button", Text: "Open Setup PR", Url: "https://github.com/" + strings.Join(parts[1:], "/"), Style: "primary"})
		err = autoSetup(ctx, client, owner, repo)
		if err != nil {
			return errors.Trace(err)
		}
	}
	payload := slack.Payload{
		Text:        "There was some activity on a Pull Request",
		Username:    "robot",
		Attachments: []slack.Attachment{attachment},
	}
	errArr := slack.Send(webhookURL, "", payload)
	if len(errArr) > 0 {
		fmt.Printf("error: %s\n", err)
	}
	return nil
}

func forPRChange(ctx context.Context, username, token string) error {
	authedClient := oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token}))
	ghc := github.NewClient(authedClient)
	ctx = context.Background()
	utc, err := time.LoadLocation("UTC")
	if err != nil {
		return errors.Trace(err)
	}

	window := 2 * time.Second

	lastPolled := time.Now().In(utc)
	var now time.Time

	for range time.NewTicker(window).C {
		now = time.Now().In(utc)
		notifications, err := getNotifications(ghc, lastPolled.Add(time.Second*-1), now)
		if err != nil {
			return errors.Trace(err)
		}

		for _, not := range notifications {
			err := notifySlack(ctx, ghc, *not)
			if err != nil {
				return errors.Trace(err)
			}
		}
		lastPolled = now
	}
	return nil
}

func autoSetup(ctx context.Context, client *github.Client, owner, repo string) error {
	rf, _, err := client.Repositories.CreateFork(ctx, owner, repo, nil)
	if err != nil {
		return errors.Trace(err)
	}
	for {
		if rf != nil {
			break
		}
	}

	r, err := git.Clone(memory.NewStorage(), nil, &git.CloneOptions{
		URL: rf.GetCloneURL(),
	})
	headRef, err := r.Head()
	if err != nil {
		return errors.Trace(err)
	}

	// Create a new plumbing.HashReference object with the name of the branch
	// and the hash from the HEAD. The reference name should be a full reference
	// name and not an abbreviated one, as is used on the git cli.
	//
	// For tags we should use `refs/tags/%s` instead of `refs/heads/%s` used
	// for branches.
	ref := plumbing.NewHashReference("refs/heads/CodeLingo-Setup", headRef.Hash())

	// The created reference is saved in the storage.
	err = r.Storer.SetReference(ref)
	if err != nil {
		return errors.Trace(err)
	}

	return nil
}
