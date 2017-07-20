package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

import (
	"github.com/hashicorp/errwrap"
	"github.com/pborman/getopt"
)

var githash = "unknown"
var buildstamp = "unknown"

var logger *log.Logger
var logFatal *log.Logger

func init() {
	logger = log.New(os.Stdout, "", log.LstdFlags)
	logFatal = log.New(os.Stderr, "", log.LstdFlags|log.Lshortfile)
}

// Main entry point to the downloader application. This will allow you to run
// the downloader as a stand-alone binary.
func main() {
	err := runSignatureUpdate(parseCliFlags())

	if err != nil {
		logFatal.Fatal(err)
	}
}

// Functional entry point to the application. Use this method to invoke the
// downloader from external code.
func runSignatureUpdate(verboseMode bool, dataFilePath string, downloadMirrorURL string,
	diffCountThreshold uint16) error {
	logger.Println("Updating ClamAV signatures")

	if verboseMode {
		logger.Printf("Data file directory: %v", dataFilePath)
	}

	sigtoolPath, err := findSigtoolPath()

	if err != nil {
		return err
	}

	if verboseMode {
		logger.Printf("ClamAV executable sigtool found at path: %v", sigtoolPath)
	}

	mirrorDomain := "current.cvd.clamav.net"
	mirrorTxtRecord, err := pullTxtRecord(mirrorDomain)

	if err != nil {
		return err
	}

	if verboseMode {
		logger.Printf("TXT record for [%v]: %v", mirrorDomain, mirrorTxtRecord)
	}

	versions, err := parseTxtRecord(mirrorTxtRecord)

	if err != nil {
		return err
	}

	if verboseMode {
		logger.Printf("TXT record values parsed: %v", versions)
	}

	var signaturesToUpdate = [3]Signature{
		{Name: "main", Version: versions.MainVersion},
		{Name: "daily", Version: versions.DailyVersion},
		{Name: "bytecode", Version: versions.ByteCodeVersion},
	}

	for _, signature := range signaturesToUpdate {
		err = updateFile(verboseMode, dataFilePath, sigtoolPath, signature,
			downloadMirrorURL, diffCountThreshold)

		if err != nil {
			return err
		}
	}

	return nil
}

// Function that parses the CLI options passed to the application.
func parseCliFlags() (bool, string, string, uint16) {
	verbosePart := getopt.BoolLong("verbose", 'v',
		"Enable verbose mode with additional debugging information")
	versionPart := getopt.BoolLong("version", 'V',
		"Display the version and exit")
	dataFilePart := getopt.StringLong("data-file-path", 'd',
		"/var/clamav/data", "Path to ClamAV data files")
	diffThresholdPart := getopt.Uint16Long("diff-count-threshold", 't',
		100, "Number of diffs to download until we redownload the signature files")
	downloadMirrorPart := getopt.StringLong("download-mirror-url", 'm',
		"http://database.clamav.net", "URL to download signature updates from")

	getopt.Parse()

	if *versionPart {
		fmt.Println("sigupdate")
		fmt.Println("")
		fmt.Printf("License        : MPLv2\n")
		fmt.Printf("Git Commit Hash: %v\n", githash)
		fmt.Printf("UTC Build Time : %v\n", buildstamp)

		os.Exit(0)
	}

	if !exists(*dataFilePart) {
		msg := fmt.Sprintf("Data file path doesn't exist or isn't accessible: %v",
			*dataFilePart)
		logFatal.Fatal(msg)
	}

	dataFileAbsPath, err := filepath.Abs(*dataFilePart)

	if err != nil {
		msg := fmt.Sprintf("Unable to parse absolute path of data file path: %v",
			*dataFilePart)
		logFatal.Fatal(msg)
	}

	if !isWritable(dataFileAbsPath) {
		msg := fmt.Sprintf("Data file path doesn't have write access for "+
			"current user at path: %v", dataFileAbsPath)
		logFatal.Fatal(msg)
	}

	return *verbosePart, dataFileAbsPath, *downloadMirrorPart, *diffThresholdPart
}

// Function that gets retrieves the value of the DNS TXT record published by
// ClamAV.
func pullTxtRecord(mirrorDomain string) (string, error) {
	mirrorTxtRecords, err := net.LookupTXT(mirrorDomain)

	if err != nil {
		msg := fmt.Sprintf("Unable to resolve TXT record for %v. {{err}}", mirrorDomain)
		return "", errwrap.Wrapf(msg, err)
	}

	if len(mirrorTxtRecords) < 1 {
		msg := fmt.Sprintf("No TXT records returned for %v. {{err}}", mirrorDomain)
		return "", errwrap.Wrapf(msg, err)
	}

	return mirrorTxtRecords[0], nil
}

// Function that parses the DNS TXT record published by ClamAV for the latest
// signature versions.
func parseTxtRecord(mirrorTxtRecord string) (SignatureVersions, error) {
	var versions SignatureVersions

	s := strings.SplitN(mirrorTxtRecord, ":", 8)

	mainv, err := strconv.ParseInt(s[1], 10, 64)

	if err != nil {
		return versions, errwrap.Wrapf("Error parsing main version. {{err}}", err)
	}

	daily, err := strconv.ParseInt(s[2], 10, 64)

	if err != nil {
		return versions, errwrap.Wrapf("Error parsing daily version. {{err}}", err)
	}

	safebrowsingv, err := strconv.ParseInt(s[6], 10, 64)

	if err != nil {
		return versions, errwrap.Wrapf("Error parsing safe browsing version. {{err}}", err)
	}

	bytecodev, err := strconv.ParseInt(s[7], 10, 64)

	if err != nil {
		return versions, errwrap.Wrapf("Error parsing bytecode version. {{err}}", err)
	}

	versions = SignatureVersions{
		MainVersion:         mainv,
		DailyVersion:        daily,
		SafeBrowsingVersion: safebrowsingv,
		ByteCodeVersion:     bytecodev,
	}

	return versions, nil
}

// Function that finds the path to the sigtool utility on the local system.
func findSigtoolPath() (string, error) {
	execName := "sigtool"
	separator := string(os.PathSeparator)
	envPathSeparator := string(os.PathListSeparator)
	envPath := os.Getenv("PATH")
	localPath := "." + separator + execName

	if exists(localPath) {
		execPath, err := filepath.Abs(localPath)

		if err != nil {
			msg := fmt.Sprintf("Error parsing absolute path for [%v]. {{err}}", localPath)
			return "", errwrap.Wrapf(msg, err)
		}

		return execPath, nil
	}

	for _, pathElement := range strings.Split(envPath, envPathSeparator) {
		execPath := pathElement + separator + execName

		if exists(execPath) {
			return execPath, nil
		}
	}

	err := errors.New("The ClamAV executable sigtool was not found in the " +
		"current directory nor in the system path.")

	return "", err
}

// Function that updates the data files for a given signature by either
// downloading the datafile or downloading diffs.
func updateFile(verboseMode bool, dataFilePath string, sigtoolPath string,
	signature Signature, downloadMirrorURL string, diffCountThreshold uint16) error {
	filePrefix := signature.Name
	currentVersion := signature.Version
	separator := string(filepath.Separator)

	filename := filePrefix + ".cvd"
	localFilePath := dataFilePath + separator + filename

	// Download the signatures for the first time if they don't exist
	if !exists(localFilePath) {
		logger.Printf("Local copy of [%v] does not exist - initiating download.",
			localFilePath)
		_, err := downloadFile(verboseMode, filename, localFilePath, downloadMirrorURL)

		if err != nil {
			return err
		}

		return nil
	}

	if verboseMode {
		logger.Printf("Local copy of [%v] already exists - "+
			"initiating diff based update", localFilePath)
	}

	oldVersion, err := findLocalVersion(localFilePath, sigtoolPath)

	if err != nil || oldVersion < 0 {
		logger.Printf("There was a problem with the version [%v] of file [%v]. "+
			"The file will be downloaded again. Original Error: %v", oldVersion, localFilePath, err)
		_, err := downloadFile(verboseMode, filename, localFilePath, downloadMirrorURL)

		if err != nil {
			return err
		}

		return nil
	}

	if verboseMode {
		logger.Printf("%v current version: %v", filename, oldVersion)
	}

	/* Attempt to download a diff for each version until we reach the current
	 * version. */
	for count := oldVersion + 1; count <= currentVersion; count++ {
		diffFilename := filePrefix + "-" + strconv.FormatInt(count, 10) + ".cdiff"
		localDiffFilePath := dataFilePath + separator + diffFilename

		// Don't bother downloading a diff if it already exists
		if exists(localDiffFilePath) {
			if verboseMode {
				logger.Printf("Local copy of [%v] already exists, not downloading",
					localDiffFilePath)
			}
			continue
		}

		_, err := downloadFile(verboseMode, diffFilename, localDiffFilePath, downloadMirrorURL)

		/* Give up attempting to download incremental diffs if we can't find a
		 * diff file corresponding to the version needed. We just go download
		 * the main signature file again if we hit this case. */
		if err != nil {
			logger.Printf("There was a problem downloading diff [%v] of file [%v]. "+
				"The file original file [%v] will be downloaded again. Original Error: %v",
				count, diffFilename, filename, err)

			_, err := downloadFile(verboseMode, filename, localFilePath, downloadMirrorURL)

			if err != nil {
				return err
			}
			break
		}
	}

	/* If we have too many diffs, we go ahead and download the whole signatures
	 * after we have the diffs so that our base signature files stay relatively
	 * current. */
	if currentVersion-oldVersion > int64(diffCountThreshold) {
		logger.Printf("Original signature has deviated beyond threshold from diffs, "+
			"so we are downloading the file [%v] again", filename)

		_, err := downloadFile(verboseMode, filename, localFilePath, downloadMirrorURL)

		if err != nil {
			return err
		}
	}

	return nil
}

// Function that uses the ClamAV sigtool executable to extract the version number
// from a signature definition file.
func findLocalVersion(localFilePath string, sigtoolPath string) (int64, error) {
	versionDelim := "Version:"
	errVersion := int64(-1)

	cmd := exec.Command(sigtoolPath, "-i", localFilePath)
	stdout, err := cmd.StdoutPipe()

	defer stdout.Close()

	if err != nil {
		return errVersion, errwrap.Wrapf("Error instantiating sigtool command. {{err}}", err)
	}

	if err := cmd.Start(); err != nil {
		return errVersion, errwrap.Wrapf("Error running sigtool. {{err}}", err)
	}

	scanner := bufio.NewScanner(stdout)
	var version int64 = math.MinInt64
	validated := false

	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, versionDelim) {
			s := strings.SplitAfter(line, versionDelim+" ")
			versionString := strings.TrimSpace(s[1])
			parsedVersion, err := strconv.ParseInt(versionString, 10, 64)

			if err != nil {
				msg := fmt.Sprintf("Error converting [%v] to 64-bit integer. {{err}}",
					versionString)
				return errVersion, errwrap.Wrapf(msg, err)
			}

			version = parsedVersion
		}

		if strings.HasPrefix(line, "Verification OK") {
			validated = true
		}
	}

	if !validated {
		return errVersion, errors.New("The file was not reported as validated")
	}

	if version == math.MinInt64 {
		return errVersion, errors.New("No version information was available for file")
	}

	if err := scanner.Err(); err != nil {
		return errVersion, errwrap.Wrapf("Error parsing sigtool STDOUT", err)
	}

	if err := cmd.Wait(); err != nil {
		return errVersion, errwrap.Wrapf("Error waiting for sigtool STDOUT to flush", err)
	}

	return version, nil
}

// Function that downloads a file from the mirror URL and moves it into the
// data directory if it was successfully downloaded.
func downloadFile(verboseMode bool, filename string, localFilePath string,
	downloadMirrorURL string) (int, error) {

	unknownStatus := -1
	downloadURL := downloadMirrorURL + "/" + filename

	output, err := ioutil.TempFile(os.TempDir(), filename+"-")

	// Skip downloading the file if our local copy is newer than the remote copy
	if exists(localFilePath) {
		newer, err := checkIfRemoteIsNewer(verboseMode, localFilePath, downloadURL)

		if err != nil {
			return unknownStatus, err
		}

		if newer {
			return unknownStatus, nil
		}
	}

	if verboseMode {
		logger.Printf("Downloading to temporary file: [%v]", output.Name())
	}

	if err != nil {
		msg := fmt.Sprintf("Unable to create file: [%v]. {{err}}", output.Name())
		return unknownStatus, errwrap.Wrapf(msg, err)
	}

	defer output.Close()

	response, err := http.Get(downloadURL)

	if err != nil {
		msg := fmt.Sprintf("Unable to retrieve file from: [%v]. {{err}}", downloadURL)
		return unknownStatus, errwrap.Wrapf(msg, err)
	}

	if response.StatusCode != http.StatusOK {
		msg := fmt.Sprintf("Unable to download file: [%v]", response.Status)
		return response.StatusCode, errors.New(msg)
	}

	lastModified, err := http.ParseTime(response.Header.Get("Last-Modified"))

	if err != nil {
		logger.Printf("Error parsing last-modified header [%v] for file: %v",
			response.Header.Get("Last-Modified"), downloadURL)
		lastModified = time.Now()
	}

	defer response.Body.Close()

	n, err := io.Copy(output, response.Body)

	if err != nil {
		msg := fmt.Sprintf("Error copying data from URL [%v] to local file [%v]. {{err}}",
			downloadURL, localFilePath)
		return response.StatusCode, errwrap.Wrapf(msg, err)
	}

	os.Rename(output.Name(), localFilePath)
	/* Change the last modified time so that we have a record that corresponds to the
	 * server's timestamps. */
	os.Chtimes(localFilePath, lastModified, lastModified)

	logger.Printf("Download complete: %v --> %v [%v bytes]", downloadURL, localFilePath, n)

	return response.StatusCode, nil
}

// Function that checks to see if the remote file is newer than the locally stored
// file.
func checkIfRemoteIsNewer(verboseMode bool, localFilePath string, downloadURL string) (bool, error) {
	localFileStat, err := os.Stat(localFilePath)

	if err != nil {
		return true, errwrap.Wrapf("Unable to stat file. {{err}}", err)
	}

	localModTime := localFileStat.ModTime()
	response, err := http.Head(downloadURL)

	if err != nil {
		msg := fmt.Sprintf("Unable to complete HEAD request: [%v]. {{err}}", downloadURL)
		return true, errwrap.Wrapf(msg, err)
	}

	remoteModTime, err := http.ParseTime(response.Header.Get("Last-Modified"))

	if verboseMode {
		logger.Printf("Local file [%v] last-modified: %v", downloadURL, localModTime)
		logger.Printf("Remote file [%v] last-modified: %v", downloadURL, remoteModTime)
	}

	if err != nil {
		msg := fmt.Sprintf("Error parsing last-modified header [%v] for file [%v]. {{err}}",
			response.Header.Get("Last-Modified"), downloadURL)
		return true, errwrap.Wrapf(msg, err)
	}

	if localModTime.After(remoteModTime) {
		logger.Printf("Skipping download of [%v] because local copy is newer", downloadURL)
		return false, nil
	}

	return true, nil
}
