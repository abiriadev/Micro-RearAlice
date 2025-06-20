package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/ini.v1"
)

type Backlink struct {
	Document string `json:"document"`
	Flags    string `json:"flags"`
}

type BacklinkResponse struct {
	Backlinks []Backlink `json:"backlinks"`
}

type Discuss struct {
	Slug        string `json:"slug"`
	Topic       string `json:"topic"`
	UpdatedDate int    `json:"updated_date"`
	Status      string `json:"status"`
}

func main() {
	cfg, err := ini.Load("config.ini")
	if err != nil {
		cfg = ini.Empty()
		domain, token := promptConfig()
		cfg.Section("").Key("domain").SetValue(domain)
		cfg.Section("").Key("token").SetValue(token)
		cfg.SaveTo("config.ini")
	}
	domain := cfg.Section("").Key("domain").String()
	token := cfg.Section("").Key("token").String()

	dataCfg, err := ini.Load("data.ini")
	if err != nil {
		dataCfg = ini.Empty()
		nsInput := prompt("Enter namespaces to search (comma-separated): ")
		logTpl := prompt("Enter log template (use {old} and {new}): ")
		watchDoc := prompt("Enter document to watch for open discussion: ")
		dataCfg.Section("").Key("namespaces").SetValue(nsInput)
		dataCfg.Section("").Key("logTemplate").SetValue(logTpl)
		dataCfg.Section("").Key("watchDocument").SetValue(watchDoc)
		dataCfg.SaveTo("data.ini")
	}
	nsList := parseList(dataCfg.Section("").Key("namespaces").String())
	logTemplate := dataCfg.Section("").Key("logTemplate").String()
	watchDocument := dataCfg.Section("").Key("watchDocument").String()

	go func() {
		for {
			open, err := checkDiscuss(domain, token, watchDocument)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error checking discuss: %v\n", err)
				panic(err)
			} else if open {
				fmt.Printf("Discuss on '%s' is normal. Stopping bot.\n", watchDocument)
				os.Exit(0)
			}
			time.Sleep(15 * time.Second)
		}
	}()

	oldTitle := prompt("Enter old title: ")
	newTitle := prompt("Enter new title: ")
	keepText := strings.ToLower(prompt("Keep display text for bare links? (y/n): ")) == "y"

	logEntry := strings.ReplaceAll(logTemplate, "{old}", oldTitle)
	logEntry = strings.ReplaceAll(logEntry, "{new}", newTitle)

	docsMap := make(map[string]struct{})
	for _, ns := range nsList {
		list, err := getBacklinksByNamespace(domain, token, oldTitle, ns)
		if err != nil {
			fmt.Printf("Error fetching backlinks in namespace '%s': %v\n", ns, err)
			continue
		}
		for _, doc := range list {
			docsMap[doc] = struct{}{}
		}
	}
	var docs []string
	for doc := range docsMap {
		docs = append(docs, doc)
	}
	total := len(docs)
	fmt.Printf("Found %d backlinks to process.\n", total)

	re := regexp.MustCompile(`\[\[[\t\f ]*` + regexp.QuoteMeta(oldTitle) + `[\t\f ]*(?:\|([^\[\]]+))?\]\]`)
	for idx, doc := range docs {
		text, editToken, err := getPageContent(domain, token, doc)
		if err != nil {
			if err == ErrPermDenied {
				fmt.Printf("권한 문제로 %s 문서를 편집할 수 없습니다. (%d/%d).\n", doc, idx+1, total)
			} else {
				fmt.Printf("Failed to fetch %s (%d/%d): %v\n", doc, idx+1, total, err)
			}
			continue
		}
		updated := re.ReplaceAllStringFunc(text, func(m string) string {
			parts := re.FindStringSubmatch(m)
			if parts[1] == newTitle {
				parts[1] = ""
			}
			if parts[1] != "" {
				return fmt.Sprintf("[[%s|%s]]", newTitle, parts[1])
			}
			if keepText {
				return fmt.Sprintf("[[%s|%s]]", newTitle, oldTitle)
			}
			return fmt.Sprintf("[[%s]]", newTitle)
		})
		if updated != text {
			err = updatePageContent(domain, token, doc, updated, editToken, logEntry)
			if err != nil {
				fmt.Printf("Failed to update %s (%d/%d): %v\n", doc, idx+1, total, err)
			} else {
				fmt.Printf("Updated %s (%d/%d)\n", doc, idx+1, total)
			}
			time.Sleep(time.Second)
		}
	}
}

func promptConfig() (string, string) {
	d := prompt("Enter domain (e.g. theseed.io): ")
	t := prompt("Enter API token: ")
	return d, t
}

func prompt(msg string) string {
	fmt.Print(msg)
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	return strings.TrimSpace(line)
}

func parseList(s string) []string {
	parts := strings.Split(s, ",")
	var list []string
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			list = append(list, t)
		}
	}
	return list
}

func getBacklinksByNamespace(domain, token, title, namespace string) ([]string, error) {
	urlStr := fmt.Sprintf("https://%s/api/backlink/%s?namespace=%s", domain,
		url.PathEscape(title), url.QueryEscape(namespace))
	req, _ := http.NewRequest("GET", urlStr, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	client := http.DefaultClient
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var res BacklinkResponse
	json.Unmarshal(body, &res)
	var docs []string
	for _, b := range res.Backlinks {
		if b.Flags == "link" {
			docs = append(docs, b.Document)
		}
	}
	return docs, nil
}

func checkDiscuss(domain, token, title string) (bool, error) {
	urlStr := fmt.Sprintf("https://%s/api/discuss/%s", domain, url.PathEscape(title))
	req, _ := http.NewRequest("GET", urlStr, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	client := http.DefaultClient
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	var discussList []Discuss
	body, _ := io.ReadAll(resp.Body)
	json.Unmarshal(body, &discussList)

	for _, d := range discussList {
		if d.Status == "normal" {
			return true, nil
		}
	}

	return false, nil
}

var ErrPermDenied = errors.New("API access denied due to insufficient permissions")

func getPageContent(domain, token, title string) (string, string, error) {
	urlStr := fmt.Sprintf("https://%s/api/edit/%s", domain, url.PathEscape(title))
	req, _ := http.NewRequest("GET", urlStr, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	client := http.DefaultClient
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var r struct {
		Text   string `json:"text"`
		Token  string `json:"token"`
		Status string `json:"status"`
	}
	json.Unmarshal(body, &r)
	if strings.Contains(r.Status, "때문에 편집 권한이 부족합니다.") {
		return "", "", ErrPermDenied
	}
	return r.Text, r.Token, nil
}

func updatePageContent(domain, token, title, content, editToken, logMsg string) error {
	payload := map[string]string{"text": content, "log": logMsg, "token": editToken}
	data, _ := json.Marshal(payload)
	urlStr := fmt.Sprintf("https://%s/api/edit/%s", domain, url.PathEscape(title))
	req, _ := http.NewRequest("POST", urlStr, strings.NewReader(string(data)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := http.DefaultClient
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("status %s", resp.Status)
	}
	return nil
}
