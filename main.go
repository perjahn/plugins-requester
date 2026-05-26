package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
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
	Size    int64
	Sha256  string
}

func main() {
	includeOptional := flag.Bool("o", false, "include optional dependencies")
	flag.Parse()

	additionalplugins := flag.Args()

	existingVersions := readExistingVersions()

	suggested, err := getSuggestedPlugins()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	plugins, err := getJenkinsPlugins(append(suggested, additionalplugins...), *includeOptional)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	var newPlugins []Plugin
	for _, plugin := range plugins {
		key := fmt.Sprintf("%s@%s", plugin.Name, plugin.Version)
		if _, exists := existingVersions[key]; !exists {
			newPlugins = append(newPlugins, plugin)
		}
	}
	plugins = newPlugins

	if len(plugins) == 0 {
		fmt.Println("All plugins are already downloaded. Nothing to do.")
		return
	}

	downloadPlugins(plugins)

	sort.Slice(plugins, func(i, j int) bool {
		return plugins[i].Name < plugins[j].Name
	})

	file, err := os.OpenFile("versions.txt", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		fmt.Printf("Error opening versions.txt: %v\n", err)
		os.Exit(1)
	}
	defer file.Close()

	var totalSize int64
	for _, plugin := range plugins {
		fmt.Printf("Name: '%s', Version: '%s', Url: '%s', Size: %d, Sha256: '%s'\n", plugin.Name, plugin.Version, plugin.Url, plugin.Size, plugin.Sha256)
		totalSize += plugin.Size
		fmt.Fprintf(file, "%s@%s\n", plugin.Name, plugin.Version)
	}

	fmt.Printf("\nTotal plugins: %d, Total downloaded: %d bytes\n", len(plugins), totalSize)
}

func readExistingVersions() map[string]bool {
	existing := make(map[string]bool)

	data, err := os.ReadFile("versions.txt")
	if err != nil {
		return existing
	}

	lines := strings.SplitSeq(string(data), "\n")
	for line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			existing[line] = true
		}
	}

	return existing
}

func getSuggestedPlugins() ([]string, error) {
	url := "https://raw.githubusercontent.com/jenkinsci/jenkins/refs/heads/master/core/src/main/resources/jenkins/install/platform-plugins.json"

	resp, err := http.DefaultClient.Get(url)
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

func getJenkinsPlugins(pluginNames []string, includeOptional bool) ([]Plugin, error) {
	url := "https://westeurope.cloudflare.jenkins.io/current/update-center.actual.json"

	resp, err := http.DefaultClient.Get(url)
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
	for _, suggested := range pluginNames {
		plugin := jenkinsPlugins.Plugins[suggested]
		getDependencies(jenkinsPlugins, plugin.Name, plugins, includeOptional)
	}

	var result []Plugin
	for _, plugin := range plugins {
		result = append(result, plugin)
	}

	return result, nil
}

func getDependencies(allPlugins JenkinsPlugins, pluginName string, plugins map[string]Plugin, includeOptional bool) {
	stack := []string{pluginName}

	for len(stack) > 0 {
		currentName := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if _, exists := plugins[currentName]; exists {
			continue
		}

		plugin := allPlugins.Plugins[currentName]

		for _, dependency := range plugin.Dependencies {
			if !dependency.Optional || includeOptional {
				stack = append(stack, dependency.Name)
			}
		}

		plugins[currentName] = Plugin{
			Name:    plugin.Name,
			Url:     plugin.Url,
			Version: plugin.Version,
			Size:    0,
		}
	}
}

func downloadPlugins(plugins []Plugin) {
	type downloadResult struct {
		index  int
		url    string
		size   int64
		sha256 string
		err    error
	}

	results := make(chan downloadResult, len(plugins))
	var wg sync.WaitGroup

	for i, plugin := range plugins {
		wg.Add(1)
		go func(index int, pluginUrl string, pluginName string) {
			defer wg.Done()
			finalUrl, size, sha256sum, err := downloadAndChecksum(pluginUrl)
			if err != nil {
				fmt.Printf("Error downloading %s: %v\n", pluginName, err)
				results <- downloadResult{index, pluginUrl, 0, "", err}
			} else {
				results <- downloadResult{index, finalUrl, size, sha256sum, nil}
			}
		}(i, plugin.Url, plugin.Name)
	}

	wg.Wait()
	close(results)

	for result := range results {
		if result.err == nil {
			plugins[result.index].Sha256 = result.sha256
			plugins[result.index].Size = result.size
		}
	}
}

func downloadAndChecksum(url string) (string, int64, string, error) {
	resp, err := http.DefaultClient.Get(url)
	if err != nil {
		return url, 0, "", err
	}
	defer resp.Body.Close()

	hash := sha256.New()
	size, err := io.Copy(hash, resp.Body)
	if err != nil {
		return url, 0, "", err
	}

	sha256sum := hex.EncodeToString(hash.Sum(nil))
	return resp.Request.URL.String(), size, sha256sum, nil
}
