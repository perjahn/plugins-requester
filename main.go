package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"sync"
)

type SuggestedJenkinsPluginsGroup struct {
	Category string                   `json:"category"`
	Plugins  []SuggestedJenkinsPlugin `json:"plugins"`
}

type SuggestedJenkinsPlugin struct {
	Name      string `json:"name"`
	Suggested bool   `json:"suggested"`
	Added     string `json:"added"`
}

type JenkinsPlugins struct {
	Plugins map[string]JenkinsPlugin `json:"plugins"`
}

type JenkinsPlugin struct {
	BuildDate     string              `json:"buildDate"`
	DefaultBranch string              `json:"defaultBranch"`
	Dependencies  []JenkinsDependency `json:"dependencies"`
	Name          string              `json:"name"`
	Url           string              `json:"url"`
	Version       string              `json:"version"`
}

type JenkinsDependency struct {
	Name     string `json:"name"`
	Optional bool   `json:"optional"`
	Version  string `json:"version"`
}

type Plugin struct {
	Name    string
	Url     string
	Version string
}

func main() {
	includeOptional := flag.Bool("o", false, "include optional dependencies")
	flag.Parse()

	additionalplugins := flag.Args()

	suggested, err := getSuggestedPlugins()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	plugins, err := getJenkinsPluginUrls(append(suggested, additionalplugins...), *includeOptional)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	type redirectResult struct {
		index int
		url   string
		err   error
	}

	results := make(chan redirectResult, len(plugins))
	var wg sync.WaitGroup

	for i, plugin := range plugins {
		wg.Add(1)
		go func(index int, pluginURL string, pluginName string) {
			defer wg.Done()
			redirectUrl, err := resolveRedirect(pluginURL)
			if err != nil {
				fmt.Printf("Error resolving redirect for %s: %v\n", pluginName, err)
				results <- redirectResult{index, pluginURL, err}
			} else {
				results <- redirectResult{index, redirectUrl, nil}
			}
		}(i, plugin.Url, plugin.Name)
	}

	wg.Wait()
	close(results)

	for result := range results {
		if result.err == nil && result.url != plugins[result.index].Url {
			plugins[result.index].Url = result.url
		}
	}

	sort.Slice(plugins, func(i, j int) bool {
		return plugins[i].Name < plugins[j].Name
	})

	for _, plugin := range plugins {
		fmt.Printf("Name: '%s', Version: '%s', Url: '%s'\n", plugin.Name, plugin.Version, plugin.Url)
	}
}

func getSuggestedPlugins() ([]string, error) {
	client := &http.Client{}

	url := "https://raw.githubusercontent.com/jenkinsci/jenkins/refs/heads/master/core/src/main/resources/jenkins/install/platform-plugins.json"

	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("error downloading list of suggested plugins: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Printf("Url: '%s'\n", url)
		fmt.Printf("Unexpected status: '%s'\n", resp.Status)
		body, _ := io.ReadAll(resp.Body)
		fmt.Printf("Response body: '%s'\n", body)
		return nil, fmt.Errorf("error downloading list of suggested plugins")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading response body: %w", err)
	}

	var groups []SuggestedJenkinsPluginsGroup
	err = json.Unmarshal(body, &groups)
	if err != nil {
		return nil, fmt.Errorf("error parsing json: %w", err)
	}

	var suggested []string
	for _, group := range groups {
		for _, plugin := range group.Plugins {
			if plugin.Suggested {
				suggested = append(suggested, plugin.Name)
			}
		}
	}

	return suggested, nil
}

func getJenkinsPluginUrls(pluginids []string, includeOptional bool) ([]Plugin, error) {
	client := &http.Client{}

	url := "https://westeurope.cloudflare.jenkins.io/current/update-center.actual.json"

	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("error downloading list of plugins: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Printf("Url: '%s'\n", url)
		fmt.Printf("Unexpected status: '%s'\n", resp.Status)
		body, _ := io.ReadAll(resp.Body)
		fmt.Printf("Response body: '%s'\n", body)
		return nil, fmt.Errorf("error downloading list of plugins")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading response body: %w", err)
	}

	var jenkinsPlugins JenkinsPlugins
	err = json.Unmarshal(body, &jenkinsPlugins)
	if err != nil {
		return nil, fmt.Errorf("error parsing json: %w", err)
	}

	plugins := make(map[string]Plugin)
	for _, suggested := range pluginids {
		plugin := jenkinsPlugins.Plugins[suggested]
		getDependencies(jenkinsPlugins, plugin.Name, plugins, includeOptional)
	}

	var result []Plugin
	for _, plugin := range plugins {
		result = append(result, plugin)
	}

	return result, nil
}

func getDependencies(allPlugins JenkinsPlugins, pluginId string, plugins map[string]Plugin, includeOptional bool) {
	if _, exists := plugins[pluginId]; exists {
		return
	}

	plugin := allPlugins.Plugins[pluginId]
	for _, dependency := range plugin.Dependencies {
		if !dependency.Optional || includeOptional {
			getDependencies(allPlugins, dependency.Name, plugins, includeOptional)
		}
	}

	plugins[pluginId] = Plugin{
		Name:    plugin.Name,
		Url:     plugin.Url,
		Version: plugin.Version,
	}
}

func resolveRedirect(url string) (string, error) {
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Head(url)
	if err != nil {
		return url, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusMovedPermanently || resp.StatusCode == http.StatusFound {
		location := resp.Header.Get("Location")
		if location != "" {
			return location, nil
		}
	}

	return url, nil
}
