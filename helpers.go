package gobrew

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Masterminds/semver"
	"github.com/c4milo/unpackit"
	"github.com/gookit/color"

	"github.com/kevincobain2000/gobrew/utils"
)

func (gb *GoBrew) getLatestVersion() string {
	getGolangVersions := gb.getGolangVersions()
	// loop through reverse and ignore beta and rc versions to get latest version
	for i := len(getGolangVersions) - 1; i >= 0; i-- {
		r := regexp.MustCompile("beta.*|rc.*")
		matches := r.FindAllString(getGolangVersions[i], -1)
		if len(matches) == 0 {
			return getGolangVersions[i]
		}
	}
	return ""
}

func (gb *GoBrew) getArch() string {
	return runtime.GOOS + "-" + runtime.GOARCH
}

func (gb *GoBrew) getGroupedVersion(versions []string, shouldPrint bool) map[string][]string {
	groupedVersions := make(map[string][]string)
	for _, version := range versions {
		parts := strings.Split(version, ".")
		if len(parts) > 1 {
			majorVersion := fmt.Sprintf("%s.%s", parts[0], parts[1])
			r := regexp.MustCompile("beta.*|rc.*")
			matches := r.FindAllString(majorVersion, -1)
			if len(matches) == 1 {
				majorVersion = strings.Split(version, matches[0])[0]
			}
			if !isBlackListed(majorVersion) {
				groupedVersions[majorVersion] = append(groupedVersions[majorVersion], version)
			}
		}
	}

	// groupedVersionKeys := []string{"1", "1.1", "1.2", ..., "1.17"}
	groupedVersionKeys := make([]string, 0, len(groupedVersions))
	for groupedVersionKey := range groupedVersions {
		groupedVersionKeys = append(groupedVersionKeys, groupedVersionKey)
	}

	versionsSemantic := make([]*semver.Version, 0)
	for _, r := range groupedVersionKeys {
		if v, err := semver.NewVersion(r); err == nil {
			versionsSemantic = append(versionsSemantic, v)
		}
	}

	// sort semantic versions
	sort.Sort(semver.Collection(versionsSemantic))

	// match 1.0.0 or 2.0.0
	reTopVersion := regexp.MustCompile("[0-9]+.0.0")

	for _, versionSemantic := range versionsSemantic {
		maxPerLine := 0
		strKey := versionSemantic.String()
		lookupKey := ""
		versionParts := strings.Split(strKey, ".")

		// prepare lookup key for the grouped version map.
		// 1.0.0 -> 1.0, 1.1.1 -> 1.1
		lookupKey = versionParts[0] + "." + versionParts[1]
		// On match 1.0.0, print 1. On match 2.0.0 print 2
		if reTopVersion.MatchString(strKey) {
			if shouldPrint {
				color.Infop(versionParts[0])
			}
			gb.print("\t", shouldPrint)
		} else {
			if shouldPrint {
				color.Successp(lookupKey)
			}
			gb.print("\t", shouldPrint)
		}

		groupedVersionsSemantic := make([]*semver.Version, 0)
		for _, r := range groupedVersions[lookupKey] {
			if v, err := semver.NewVersion(r); err == nil {
				groupedVersionsSemantic = append(groupedVersionsSemantic, v)
			}
		}
		// sort semantic versions
		sort.Sort(semver.Collection(groupedVersionsSemantic))

		for _, gvSemantic := range groupedVersionsSemantic {
			maxPerLine++
			if maxPerLine == 6 {
				maxPerLine = 0
				gb.print("\n\t", shouldPrint)
			}
			gb.print(gvSemantic.String()+"  ", shouldPrint)
		}

		maxPerLine = 0
		gb.print("\n\t", shouldPrint)

		// print rc and beta versions in the end
		for _, rcVersion := range groupedVersions[lookupKey] {
			r := regexp.MustCompile("beta.*|rc.*")
			matches := r.FindAllString(rcVersion, -1)
			if len(matches) == 1 {
				gb.print(rcVersion+"  ", shouldPrint)
				maxPerLine++
				if maxPerLine == 6 {
					maxPerLine = 0
					gb.print("\n\t", shouldPrint)
				}
			}
		}
		gb.print("\n", shouldPrint)
		gb.print("\n", shouldPrint)
	}
	return groupedVersions
}

func isBlackListed(version string) bool {
	blackListVersions := []string{"1.0", "1.1", "1.2", "1.3", "1.4"}
	for _, v := range blackListVersions {
		if version == v {
			return true
		}
	}
	return false
}

func (gb *GoBrew) print(message string, shouldPrint bool) {
	if shouldPrint {
		color.Infop(message)
	}
}

func (gb *GoBrew) existsVersion(version string) bool {
	path := filepath.Join(gb.versionsDir, version, "go")
	_, err := os.Stat(path)

	return err == nil
}

func (gb *GoBrew) cleanVersionDir(version string) {
	_ = os.RemoveAll(gb.getVersionDir(version))
}

func (gb *GoBrew) cleanDownloadsDir() {
	_ = os.RemoveAll(gb.downloadsDir)
}

func (gb *GoBrew) judgeVersion(version string) string {
	judgedVersion := NoneVersion
	rcBetaOk := false
	reRcOrBeta := regexp.MustCompile("beta.*|rc.*")

	// check if version string ends with x
	if strings.HasSuffix(version, "x") {
		judgedVersion = strings.TrimSuffix(version, "x")
	}

	if strings.HasSuffix(version, ".x") {
		judgedVersion = strings.TrimSuffix(version, ".x")
	}
	if strings.HasSuffix(version, "@latest") {
		judgedVersion = strings.TrimSuffix(version, "@latest")
	}
	if strings.HasSuffix(version, "@dev-latest") {
		judgedVersion = strings.TrimSuffix(version, "@dev-latest")
		rcBetaOk = true
	}

	if version == "mod" {
		// get version by reading the mod file of Go
		modVersion := gb.getModVersion()
		// if modVersion is like 1.19, 1.20, 1.21 then appened @latest to it
		if strings.Count(modVersion, ".") == 1 {
			modVersion += "@latest"
		}
		return gb.judgeVersion(modVersion)
	}
	groupedVersions := gb.ListRemoteVersions(false) // donot print

	// latest will pick the latest version excluding rc and beta
	// dev-latest will first remove rc and beta from the list of versions and then pick the latest version
	if version == "latest" || version == "dev-latest" {
		groupedVersionKeys := make([]string, 0, len(groupedVersions))
		for groupedVersionKey := range groupedVersions {
			groupedVersionKeys = append(groupedVersionKeys, groupedVersionKey)
		}
		versionsSemantic := make([]*semver.Version, 0)
		for _, r := range groupedVersionKeys {
			if v, err := semver.NewVersion(r); err == nil {
				versionsSemantic = append(versionsSemantic, v)
			}
		}
		if len(versionsSemantic) == 0 {
			return NoneVersion
		}

		// sort semantic versions
		sort.Sort(semver.Collection(versionsSemantic))
		// loop in reverse
		for i := len(versionsSemantic) - 1; i >= 0; i-- {
			judgedVersions := groupedVersions[versionsSemantic[i].Original()]
			// get last element
			if version == "dev-latest" {
				if len(judgedVersions) == 0 {
					return NoneVersion
				}
				// Filter versions containing "rc" or "beta"
				filteredVersions := gb.filterVersions(judgedVersions, []string{"rc", "beta"})

				// Get the last element of the filtered slice
				var lastVersion string
				if len(filteredVersions) > 0 {
					lastVersion = filteredVersions[len(filteredVersions)-1]
				}

				return lastVersion
			}

			// loop in reverse
			for j := len(judgedVersions) - 1; j >= 0; j-- {
				matches := reRcOrBeta.FindAllString(judgedVersions[j], -1)
				if len(matches) == 0 {
					return judgedVersions[j]
				}
			}
		}

		latest := versionsSemantic[len(versionsSemantic)-1].String()
		return gb.judgeVersion(latest)
	}

	if judgedVersion != NoneVersion {
		// check if judgedVersion is in the groupedVersions
		if _, ok := groupedVersions[judgedVersion]; ok {
			// get last item in the groupedVersions excluding rc and beta
			// loop in reverse groupedVersions
			for i := len(groupedVersions[judgedVersion]) - 1; i >= 0; i-- {
				matches := reRcOrBeta.FindAllString(groupedVersions[judgedVersion][i], -1)
				if len(matches) == 0 {
					return groupedVersions[judgedVersion][i]
				}
			}
			if rcBetaOk {
				// return last element including beta and rc if present
				return groupedVersions[judgedVersion][len(groupedVersions[judgedVersion])-1]
			}
		}
	}

	exists := false
	for _, value := range groupedVersions {
		if slices.Contains(value, version) {
			exists = true
			break
		}
	}
	if !exists {
		return NoneVersion
	}

	return version
}

func (gb *GoBrew) hasModFile() bool {
	modFilePath := "go.mod"
	_, err := os.Stat(modFilePath)
	if err == nil {
		return true
	}
	if os.IsNotExist(err) {
		return false
	}
	return false
}

// read go.mod file and extract version
// Do not use go to get the version as go list -m -f '{{.GoVersion}}'
// Because go might not be installed
func (gb *GoBrew) getModVersion() string {
	modFilePath := "go.mod"
	modFile, err := os.Open(modFilePath)
	if err != nil {
		return NoneVersion
	}
	defer func(modFile *os.File) {
		_ = modFile.Close()
	}(modFile)

	scanner := bufio.NewScanner(modFile)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "go ") {
			return strings.TrimPrefix(line, "go ")
		}
	}

	if err = scanner.Err(); err != nil {
		color.Errorln(err)
		os.Exit(1) // nolint:gocritic
	}
	return NoneVersion
}

func (gb *GoBrew) mkDirs(version string) {
	_ = os.MkdirAll(gb.installDir, os.ModePerm)
	_ = os.MkdirAll(gb.currentDir, os.ModePerm)
	_ = os.MkdirAll(gb.versionsDir, os.ModePerm)
	_ = os.MkdirAll(gb.getVersionDir(version), os.ModePerm)
	_ = os.MkdirAll(gb.downloadsDir, os.ModePerm)
}

func (gb *GoBrew) getVersionDir(version string) string {
	return filepath.Join(gb.versionsDir, version)
}

// filterVersions returns a new slice containing only the elements that contain any of the substrings in contains
func (gb *GoBrew) filterVersions(versions []string, contains []string) []string {
	var filtered []string
	for _, version := range versions {
		for _, contain := range contains {
			if strings.Contains(version, contain) {
				filtered = append(filtered, version)
				break // Move to the next version after the first match
			}
		}
	}
	return filtered
}

func (gb *GoBrew) downloadAndExtract(version string) {
	tarName := "go" + version + "." + gb.getArch() + tarNameExt

	downloadURL, _ := url.JoinPath(gb.RegistryPathURL, tarName)
	color.Infoln("==> [Info] Downloading from:", downloadURL)

	dstDownloadDir := gb.downloadsDir
	color.Infoln("==> [Info] Downloading to:", dstDownloadDir)
	err := utils.DownloadWithProgress(downloadURL, tarName, dstDownloadDir)

	if err != nil {
		gb.cleanDownloadsDir()
		color.Errorln("==> [Error] Downloading version failed:", err)
		color.Errorln("==> [Error]: Please check connectivity to url:", downloadURL)
		os.Exit(1)
	}

	srcTar := filepath.Join(gb.downloadsDir, tarName)
	dstDir := gb.getVersionDir(version)

	color.Infoln("==> [Info] Extracting from:", srcTar)
	color.Infoln("==> [Info] Extracting to:", dstDir)

	err = gb.extract(srcTar, dstDir)
	if err != nil {
		// clean up dir
		gb.cleanVersionDir(version)
		color.Errorln("==> [Info] Extract failed:", err)
		os.Exit(1)
	}
	color.Infoln("==> [Success] Extract to", gb.getVersionDir(version))
}

func (gb *GoBrew) changeSymblinkGoBin(version string) {
	goBinDst := filepath.Join(gb.versionsDir, version, "/go/bin")
	_ = os.RemoveAll(gb.currentBinDir)
	symlink(goBinDst, gb.currentBinDir)
}

func (gb *GoBrew) changeSymblinkGo(version string) {
	_ = os.RemoveAll(gb.currentGoDir)
	versionGoDir := filepath.Join(gb.versionsDir, version, "go")
	symlink(versionGoDir, gb.currentGoDir)
}

func (gb *GoBrew) getGobrewVersion() string {
	data := doRequest(gb.GobrewVersionsURL)
	if len(data) == 0 {
		return ""
	}

	type Tag struct {
		TagName string `json:"tag_name"`
	}
	var tag Tag
	utils.CheckError(json.Unmarshal(data, &tag), "==> [Error]")

	return tag.TagName
}

func (gb *GoBrew) getGolangVersions() (result []string) {
	if result = gb.getVersionsFromCache(); len(result) > 0 {
		return result
	}

	data := doRequest(gb.GobrewTags)
	if len(data) == 0 {
		return
	}

	type Tag struct {
		Ref string `json:"ref"`
	}
	var tags []Tag
	utils.CheckError(json.Unmarshal(data, &tags), "==> [Error]")

	for _, tag := range tags {
		t := strings.ReplaceAll(tag.Ref, "refs/tags/", "")
		if strings.HasPrefix(t, "go") {
			result = append(result, strings.TrimPrefix(t, "go"))
		}
	}

	gb.saveVersionsToCache(result)

	return result
}

func doRequest(url string) (data []byte) {
	client := &http.Client{}
	request, err := http.NewRequest("GET", url, nil)
	utils.CheckError(err, "==> [Error] Cannot create request")

	request.Header.Set("User-Agent", ProgramName)

	response, err := client.Do(request)
	utils.CheckError(err, "==> [Error] Cannot get response")

	defer func(body io.ReadCloser) {
		_ = body.Close()
	}(response.Body)

	if response.StatusCode == http.StatusTooManyRequests ||
		response.StatusCode == http.StatusForbidden {
		color.Errorln("==> [Error] Rate limit exhausted")
		os.Exit(1) // nolint:gocritic
	}

	if response.StatusCode != http.StatusOK {
		color.Errorln("==> [Error] Cannot read response:", response.Status)
		os.Exit(1) // nolint:gocritic
	}

	data, err = io.ReadAll(response.Body)
	utils.CheckError(err, "==> [Error] Cannot read response Body:")

	return
}

func (gb *GoBrew) extract(srcTar string, dstDir string) error {
	// #nosec G304
	file, err := os.Open(srcTar)
	if err != nil {
		return err
	}
	err = unpackit.Unpack(file, dstDir)
	if err != nil {
		return err
	}

	return nil
}

func extractMajorVersion(version string) string {
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return ""
	}
	// remove rc and beta
	parts[1] = strings.Split(parts[1], "rc")[0]
	parts[1] = strings.Split(parts[1], "beta")[0]

	// Take the first two parts and join them back with a period to create the new version.
	majorVersion := strings.Join(parts[:2], ".")
	return majorVersion
}

func askForConfirmation(s string) bool {
	reader := bufio.NewReader(os.Stdin)

	for {
		color.Successf(s) // nolint:govet
		fmt.Print(" [y/n]: ")

		response, err := reader.ReadString('\n')
		if err != nil {
			log.Fatal(err)
		}

		response = strings.ToLower(strings.TrimSpace(response))

		switch response {
		case "y", "yes":
			return true
		case "", "n", "no":
			return false
		}
	}
}

type Cache struct {
	Timestamp string   `json:"timestamp"`
	Versions  []string `json:"versions"`
}

func (gb *GoBrew) getVersionsFromCache() []string {
	if gb.DisableCache {
		return []string{}
	}

	if _, err := os.Stat(gb.cacheFile); err == nil {
		data, e := os.ReadFile(gb.cacheFile)
		if e != nil {
			return []string{}
		}

		var cache Cache
		if e = json.Unmarshal(data, &cache); e != nil {
			return []string{}
		}

		timestamp, e := time.Parse(time.RFC3339, cache.Timestamp)
		if e != nil {
			return []string{}
		}

		// cache for gb.TTL duration
		if time.Now().UTC().After(timestamp.Add(gb.TTL)) {
			return []string{}
		}

		return cache.Versions
	}

	return []string{}
}

func (gb *GoBrew) saveVersionsToCache(versions []string) {
	if gb.DisableCache {
		return
	}

	cacheFile := filepath.Join(gb.installDir, "cache.json")
	var cache = Cache{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Versions:  versions,
	}
	data, err := json.Marshal(&cache)
	if err != nil {
		return
	}

	// #nosec G306
	_ = os.WriteFile(cacheFile, data, 0600)
}
