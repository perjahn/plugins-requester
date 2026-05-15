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
	Sha256  string
	Size    int64
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

	plugins, err := getJenkinsPlugins(append(suggested, additionalplugins...), *includeOptional)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	sort.Slice(plugins, func(i, j int) bool {
		return plugins[i].Name < plugins[j].Name
	})

	var totalSize int64
	for _, plugin := range plugins {
		fmt.Printf("Name: '%s', Version: '%s', Url: '%s', Sha256: '%s', Size: %d\n", plugin.Name, plugin.Version, plugin.Url, plugin.Sha256, plugin.Size)
		totalSize += plugin.Size
	}

	fmt.Printf("\nTotal downloaded: %d bytes\n", totalSize)
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

	resolvePluginRedirects(result)

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

func resolvePluginRedirects(plugins []Plugin) {
	type redirectResult struct {
		index  int
		url    string
		sha256 string
		size   int64
		err    error
	}

	results := make(chan redirectResult, len(plugins))
	var wg sync.WaitGroup

	for i, plugin := range plugins {
		wg.Add(1)
		go func(index int, pluginURL string, pluginName string) {
			defer wg.Done()
			finalUrl, sha256sum, size, err := downloadAndChecksum(pluginURL)
			if err != nil {
				fmt.Printf("Error downloading %s: %v\n", pluginName, err)
				results <- redirectResult{index, pluginURL, "", 0, err}
			} else {
				results <- redirectResult{index, finalUrl, sha256sum, size, nil}
			}
		}(i, plugin.Url, plugin.Name)
	}

	wg.Wait()
	close(results)

	for result := range results {
		if result.err == nil {
			plugins[result.index].Url = result.url
			plugins[result.index].Sha256 = result.sha256
			plugins[result.index].Size = result.size
		}
	}
}

func downloadAndChecksum(url string) (string, string, int64, error) {
	finalUrl := url

	for {
		resp, err := http.DefaultClient.Head(finalUrl)
		if err != nil {
			return finalUrl, "", 0, err
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusMovedPermanently || resp.StatusCode == http.StatusFound {
			location := resp.Header.Get("Location")
			if location != "" {
				finalUrl = location
				continue
			}
		}
		break
	}

	resp, err := http.DefaultClient.Get(finalUrl)
	if err != nil {
		return finalUrl, "", 0, err
	}
	defer resp.Body.Close()

	hash := sha256.New()
	size, err := io.Copy(hash, resp.Body)
	if err != nil {
		return finalUrl, "", 0, err
	}

	sha256sum := hex.EncodeToString(hash.Sum(nil))
	return finalUrl, sha256sum, size, nil
}
