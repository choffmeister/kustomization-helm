package internal

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

type HelmGenerator struct {
	Registry  string                 `yaml:"registry"`
	Chart     string                 `yaml:"chart"`
	Version   string                 `yaml:"version"`
	Name      string                 `yaml:"name"`
	Namespace string                 `yaml:"namespace"`
	Args      []string               `yaml:"args"`
	Values    map[string]interface{} `yaml:"values"`
}

func (g HelmGenerator) Generate(dir string) (*Kustomization, error) {
	url, err := retrieveHelmChartUrl(g.Registry, g.Chart, g.Version)
	if err != nil {
		return nil, err
	}

	valuesPath, err := ioutil.TempFile("", ".kustomization-generator-*-values.yaml")
	if err != nil {
		return nil, fmt.Errorf("writing temporary values file failed: %v", err)
	}
	defer os.Remove(valuesPath.Name())
	valuesBytes, err := yaml.Marshal(g.Values)
	if err != nil {
		return nil, fmt.Errorf("writing temporary values file failed: %v", err)
	}
	err = os.WriteFile(valuesPath.Name(), valuesBytes, 0o600)
	if err != nil {
		return nil, fmt.Errorf("writing temporary values file failed: %v", err)
	}

	tempDir, err := ioutil.TempDir("", ".kustomization-generator-")
	if err != nil {
		return nil, fmt.Errorf("preparing temporary folder failed: %v", err)
	}
	defer os.RemoveAll(tempDir)
	helmPath, err := exec.LookPath("helm")
	if err != nil {
		return nil, fmt.Errorf("executing helm failed: executable not found")
	}
	helmArgs := []string{
		"template",
		g.Name,
		*url,
		"--namespace", g.Namespace,
		"--output-dir", tempDir,
		"--values", valuesPath.Name(),
	}
	helmArgs = append(helmArgs, g.Args...)
	helmOutput, err := exec.Command(helmPath, helmArgs...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("executing helm failed: %v\n%s", err, string(helmOutput))
	}

	kustomization := Kustomization{
		Namespace: g.Namespace,
	}
	tempDir2 := path.Join(tempDir, g.Chart)
	includes := []regexp.Regexp{*regexp.MustCompile(`\.ya?ml$`)}
	excludes := []regexp.Regexp{}
	files, err := fileList(tempDir2, includes, excludes)
	if err != nil {
		return nil, fmt.Errorf("listing helm generated resources failed: %v", err)
	}
	for _, file := range *files {
		rel, err := filepath.Rel(tempDir2, file)
		if err != nil {
			return nil, fmt.Errorf("listing helm generated resources failed: %v", err)
		}
		kustomization.Resources = append(kustomization.Resources, rel)
	}

	err = copyDir(tempDir2, dir)
	if err != nil {
		return nil, fmt.Errorf("copying files to target failed: %v", err)
	}

	return &kustomization, nil
}

type helmRegistryIndex struct {
	ApiVersion string `yaml:"apiVersion"`
	Entries    map[string][]struct {
		ApiVersion string   `yaml:"apiVersion"`
		AppVersion string   `yaml:"appVersion"`
		Name       string   `yaml:"name"`
		Version    string   `yaml:"version"`
		Urls       []string `yaml:"urls"`
	} `yaml:"entries"`
}

func retrieveHelmChartUrl(registry string, chart string, version string) (*string, error) {
	url := strings.TrimSuffix(registry, "/") + "/index.yaml"
	req, err := http.NewRequest("GET", url, nil)
	client := &http.Client{}
	if err != nil {
		return nil, fmt.Errorf("failed to fetch registry index at %s: %v", url, err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch registry index at %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch registry index at %s: %v", url, err)
	}
	index := helmRegistryIndex{}
	err = yaml.Unmarshal(body, &index)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch registry index at %s: %v", url, err)
	}

	versions, ok := index.Entries[chart]
	if !ok {
		return nil, fmt.Errorf("chart %s could not be found", chart)
	}
	for _, entry := range versions {
		if entry.Version == version {
			if len(entry.Urls) == 0 {
				return nil, fmt.Errorf("chart %s version %s has no download urls", chart, version)
			}
			if len(entry.Urls) > 1 {
				return nil, fmt.Errorf("chart %s version %s has multiple download urls", chart, version)
			}
			result := entry.Urls[0]
			if !strings.HasPrefix(result, "http://") && !strings.HasPrefix(result, "https://") {
				result = strings.TrimSuffix(registry, "/") + "/" + strings.TrimPrefix(result, "/")
			}
			return &result, nil
		}
	}
	return nil, fmt.Errorf("chart %s version %s could not be found", chart, version)
}
