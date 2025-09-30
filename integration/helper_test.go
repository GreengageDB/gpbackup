package integration

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io/ioutil"
	"math"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/GreengageDB/gp-common-go-libs/operating"
	"github.com/GreengageDB/gpbackup/utils"
	"github.com/klauspost/compress/zstd"
	"golang.org/x/sys/unix"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var (
	examplePluginExec          string
	examplePluginTestConfig    = "/tmp/test_example_plugin_config.yaml"
	examplePluginTestBackupDir = "/tmp/plugin_dest/20180101/20180101010101"
	examplePluginTestDataFile  = filepath.Join(examplePluginTestBackupDir, "test_data")
	examplePluginTestDir       = "/tmp/plugin_dest" // hardcoded in example plugin
	testDir                    = "/tmp/helper_test/20180101/20180101010101"
	tocFile                    = fmt.Sprintf("%s/test_toc.yaml", testDir)
	backupOidFile              = fmt.Sprintf("%s/backup_test_oids", testDir)
	restoreOidFile             = fmt.Sprintf("%s/restore_test_oids", testDir)
	pipeFile                   = fmt.Sprintf("%s/test_pipe", testDir)
	dataFileFullPath           = filepath.Join(testDir, "test_data")
	errorFile                  = utils.GetErrorFilename(pipeFile)
)

const (
	defaultData  = "here is some data\n"
	expectedData = `here is some data
here is some data
here is some data
`
	expectedTOC = `dataentries:
  1:
    startbyte: 0
    endbyte: 18
  2:
    startbyte: 18
    endbyte: 36
  3:
    startbyte: 36
    endbyte: 54
`
)

func gpbackupHelperBackup(helperPath string, args ...string) *exec.Cmd {
	args = append([]string{"--backup-agent", "--oid-file", backupOidFile}, args...)
	return gpbackupHelper(helperPath, args...)
}

func gpbackupHelperRestore(helperPath string, args ...string) *exec.Cmd {
	args = append([]string{"--restore-agent", "--oid-file", restoreOidFile}, args...)
	return gpbackupHelper(helperPath, args...)
}

func gpbackupHelper(helperPath string, args ...string) *exec.Cmd {
	args = append([]string{"--toc-file", tocFile, "--pipe-file", pipeFile, "--content", "1", "--single-data-file"}, args...)
	command := exec.Command(helperPath, args...)
	err := command.Start()
	Expect(err).ToNot(HaveOccurred())
	return command
}

func buildAndInstallBinaries() string {
	_ = os.Chdir("..")
	command := exec.Command("make", "build")
	output, err := command.CombinedOutput()
	if err != nil {
		fmt.Printf("%s", output)
		Fail(fmt.Sprintf("%v", err))
	}
	_ = os.Chdir("integration")
	binDir := fmt.Sprintf("%s/bin", operating.System.Getenv("GOPATH"))
	return fmt.Sprintf("%s/gpbackup_helper", binDir)
}

var _ = Describe("gpbackup_helper end to end integration tests", func() {
	// Setup example plugin based on current working directory
	err := os.RemoveAll(examplePluginTestDir)
	Expect(err).ToNot(HaveOccurred())
	err = os.MkdirAll(examplePluginTestDir, 0777)
	Expect(err).ToNot(HaveOccurred())
	currentDir, err := os.Getwd()
	Expect(err).ToNot(HaveOccurred())
	rootDir := path.Dir(currentDir)
	examplePluginExec = path.Join(rootDir, "plugins", "example_plugin.bash")
	examplePluginTestConfigContents := fmt.Sprintf(`executablepath: %s
options:
  password: unknown`, examplePluginExec)
	f, err := os.Create(examplePluginTestConfig)
	Expect(err).ToNot(HaveOccurred())
	_, err = f.WriteString(examplePluginTestConfigContents)
	Expect(err).ToNot(HaveOccurred())
	err = f.Close()
	Expect(err).ToNot(HaveOccurred())

	BeforeEach(func() {
		err := os.RemoveAll(testDir)
		Expect(err).ToNot(HaveOccurred())
		err = os.MkdirAll(testDir, 0777)
		Expect(err).ToNot(HaveOccurred())
		err = os.RemoveAll(examplePluginTestBackupDir)
		Expect(err).ToNot(HaveOccurred())
		err = os.MkdirAll(examplePluginTestBackupDir, 0777)
		Expect(err).ToNot(HaveOccurred())
	})
	Context("backup tests", func() {
		BeforeEach(func() {
			f, _ := os.Create(backupOidFile)
			_, _ = f.WriteString("1\n2\n3\n")
			err = unix.Mkfifo(fmt.Sprintf("%s_%d", pipeFile, 1), 0700)
			if err != nil {
				Fail(fmt.Sprintf("%v", err))
			}
		})
		It("runs backup gpbackup_helper without compression", func() {
			helperCmd := gpbackupHelperBackup(gpbackupHelperPath, "--compression-level", "0", "--data-file", dataFileFullPath)
			writeToBackupPipes(defaultData)
			err := helperCmd.Wait()
			printHelperLogOnError(err)
			Expect(err).ToNot(HaveOccurred())
			assertBackupArtifacts(false)
		})
		It("runs backup gpbackup_helper with data exceeding pipe buffer size", func() {
			helperCmd := gpbackupHelperBackup(gpbackupHelperPath, "--compression-level", "0", "--data-file", dataFileFullPath)
			writeToBackupPipes(strings.Repeat("a", int(math.Pow(2, 17))))
			err := helperCmd.Wait()
			printHelperLogOnError(err)
			Expect(err).ToNot(HaveOccurred())
		})
		It("runs backup gpbackup_helper with gzip compression", func() {
			helperCmd := gpbackupHelperBackup(gpbackupHelperPath, "--compression-type", "gzip", "--compression-level", "1", "--data-file", dataFileFullPath+".gz")
			writeToBackupPipes(defaultData)
			err := helperCmd.Wait()
			printHelperLogOnError(err)
			Expect(err).ToNot(HaveOccurred())
			assertBackupArtifactsWithCompression("gzip", false)
		})
		It("runs backup gpbackup_helper with zstd compression", func() {
			helperCmd := gpbackupHelperBackup(gpbackupHelperPath, "--compression-type", "zstd", "--compression-level", "1", "--data-file", dataFileFullPath+".zst")
			writeToBackupPipes(defaultData)
			err := helperCmd.Wait()
			printHelperLogOnError(err)
			Expect(err).ToNot(HaveOccurred())
			assertBackupArtifactsWithCompression("zstd", false)
		})
		It("runs backup gpbackup_helper without compression with plugin", func() {
			helperCmd := gpbackupHelperBackup(gpbackupHelperPath, "--compression-level", "0", "--data-file", dataFileFullPath, "--plugin-config", examplePluginTestConfig)
			writeToBackupPipes(defaultData)
			err := helperCmd.Wait()
			printHelperLogOnError(err)
			Expect(err).ToNot(HaveOccurred())
			assertBackupArtifacts(true)
		})
		It("runs backup gpbackup_helper with gzip compression with plugin", func() {
			helperCmd := gpbackupHelperBackup(gpbackupHelperPath, "--compression-type", "gzip", "--compression-level", "1", "--data-file", dataFileFullPath+".gz", "--plugin-config", examplePluginTestConfig)
			writeToBackupPipes(defaultData)
			err := helperCmd.Wait()
			printHelperLogOnError(err)
			Expect(err).ToNot(HaveOccurred())
			assertBackupArtifactsWithCompression("gzip", true)
		})
		It("runs backup gpbackup_helper with zstd compression with plugin", func() {
			helperCmd := gpbackupHelperBackup(gpbackupHelperPath, "--compression-type", "zstd", "--compression-level", "1", "--data-file", dataFileFullPath+".zst", "--plugin-config", examplePluginTestConfig)
			writeToBackupPipes(defaultData)
			err := helperCmd.Wait()
			printHelperLogOnError(err)
			Expect(err).ToNot(HaveOccurred())
			assertBackupArtifactsWithCompression("zstd", true)
		})
		It("Generates error file when backup agent interrupted", FlakeAttempts(5), func() {
			helperCmd := gpbackupHelperBackup(gpbackupHelperPath, "--compression-level", "0", "--data-file", dataFileFullPath)
			waitForPipeCreation()
			err := helperCmd.Process.Signal(unix.SIGINT)
			Expect(err).ToNot(HaveOccurred())
			err = helperCmd.Wait()
			Expect(err).To(HaveOccurred())
			assertErrorsHandled()
		})
	})
	Context("restore tests", func() {
		It("runs restore gpbackup_helper without compression", func() {
			setupRestoreFiles("", false)
			helperCmd := gpbackupHelperRestore(gpbackupHelperPath, "--data-file", dataFileFullPath)
			for _, i := range []int{1, 3} {
				contents, _ := ioutil.ReadFile(fmt.Sprintf("%s_%d_0", pipeFile, i))
				Expect(string(contents)).To(Equal("here is some data\n"))
			}
			err := helperCmd.Wait()
			printHelperLogOnError(err)
			Expect(err).ToNot(HaveOccurred())
			assertNoErrors()
		})
		It("runs restore gpbackup_helper with gzip compression", func() {
			setupRestoreFiles("gzip", false)
			helperCmd := gpbackupHelperRestore(gpbackupHelperPath, "--data-file", dataFileFullPath+".gz")
			for _, i := range []int{1, 3} {
				contents, _ := ioutil.ReadFile(fmt.Sprintf("%s_%d_0", pipeFile, i))
				Expect(string(contents)).To(Equal("here is some data\n"))
			}
			err := helperCmd.Wait()
			printHelperLogOnError(err)
			Expect(err).ToNot(HaveOccurred())
			assertNoErrors()
		})
		It("runs restore gpbackup_helper with zstd compression", func() {
			setupRestoreFiles("zstd", false)
			helperCmd := gpbackupHelperRestore(gpbackupHelperPath, "--data-file", dataFileFullPath+".zst")
			for _, i := range []int{1, 3} {
				contents, _ := ioutil.ReadFile(fmt.Sprintf("%s_%d_0", pipeFile, i))
				Expect(string(contents)).To(Equal("here is some data\n"))
			}
			err := helperCmd.Wait()
			printHelperLogOnError(err)
			Expect(err).ToNot(HaveOccurred())
			assertNoErrors()
		})
		It("runs restore gpbackup_helper without compression with plugin", func() {
			setupRestoreFiles("", true)
			helperCmd := gpbackupHelperRestore(gpbackupHelperPath, "--data-file", dataFileFullPath, "--plugin-config", examplePluginTestConfig)
			for _, i := range []int{1, 3} {
				contents, _ := ioutil.ReadFile(fmt.Sprintf("%s_%d_0", pipeFile, i))
				Expect(string(contents)).To(Equal("here is some data\n"))
			}
			err := helperCmd.Wait()
			printHelperLogOnError(err)
			Expect(err).ToNot(HaveOccurred())
			assertNoErrors()
		})
		It("runs restore gpbackup_helper with gzip compression with plugin", func() {
			setupRestoreFiles("gzip", true)
			helperCmd := gpbackupHelperRestore(gpbackupHelperPath, "--data-file", dataFileFullPath+".gz", "--plugin-config", examplePluginTestConfig)
			for _, i := range []int{1, 3} {
				contents, _ := ioutil.ReadFile(fmt.Sprintf("%s_%d_0", pipeFile, i))
				Expect(string(contents)).To(Equal("here is some data\n"))
			}
			err := helperCmd.Wait()
			printHelperLogOnError(err)
			Expect(err).ToNot(HaveOccurred())
			assertNoErrors()
		})
		It("runs restore gpbackup_helper with zstd compression with plugin", func() {
			setupRestoreFiles("zstd", true)
			helperCmd := gpbackupHelperRestore(gpbackupHelperPath, "--data-file", dataFileFullPath+".zst", "--plugin-config", examplePluginTestConfig)
			for _, i := range []int{1, 3} {
				contents, _ := ioutil.ReadFile(fmt.Sprintf("%s_%d_0", pipeFile, i))
				Expect(string(contents)).To(Equal("here is some data\n"))
			}
			err := helperCmd.Wait()
			printHelperLogOnError(err)
			Expect(err).ToNot(HaveOccurred())
			assertNoErrors()
		})
		It("gpbackup_helper will not error out when plugin writes something to stderr", func() {
			setupRestoreFiles("", true)

			err := exec.Command("touch", "/tmp/GPBACKUP_PLUGIN_LOG_TO_STDERR").Run()
			Expect(err).ToNot(HaveOccurred())
			defer exec.Command("rm", "/tmp/GPBACKUP_PLUGIN_LOG_TO_STDERR").Run()

			args := []string{
				"--toc-file", tocFile,
				"--oid-file", restoreOidFile,
				"--pipe-file", pipeFile,
				"--content", "1",
				"--single-data-file",
				"--restore-agent",
				"--data-file", dataFileFullPath,
				"--plugin-config", examplePluginTestConfig}
			helperCmd := exec.Command(gpbackupHelperPath, args...)

			var outBuffer bytes.Buffer
			helperCmd.Stdout = &outBuffer
			helperCmd.Stderr = &outBuffer

			err = helperCmd.Start()
			Expect(err).ToNot(HaveOccurred())

			for _, i := range []int{1, 3} {
				contents, _ := ioutil.ReadFile(fmt.Sprintf("%s_%d_0", pipeFile, i))
				Expect(string(contents)).To(Equal("here is some data\n"))
			}

			err = helperCmd.Wait()
			printHelperLogOnError(err)
			Expect(err).ToNot(HaveOccurred())

			outputStr := outBuffer.String()
			Expect(outputStr).To(ContainSubstring("Some plugin warning"))

			assertNoErrors()
		})
		It("gpbackup_helper will not error out when plugin writes something to stderr with cluster resize", func() {
			setupRestoreFiles("", true)
			for _, i := range []int{1, 3} {
				f, _ := os.Create(fmt.Sprintf("%s_%d", examplePluginTestDataFile, i))
				f.WriteString("here is some data\n")
			}

			err := exec.Command("touch", "/tmp/GPBACKUP_PLUGIN_LOG_TO_STDERR").Run()
			Expect(err).ToNot(HaveOccurred())
			defer exec.Command("rm", "/tmp/GPBACKUP_PLUGIN_LOG_TO_STDERR").Run()

			args := []string{
				"--toc-file", tocFile,
				"--oid-file", restoreOidFile,
				"--pipe-file", pipeFile,
				"--content", "1",
				"--resize-cluster",
				"--orig-seg-count", "6",
				"--dest-seg-count", "3",
				"--restore-agent",
				"--data-file", examplePluginTestDataFile,
				"--plugin-config", examplePluginTestConfig}
			helperCmd := exec.Command(gpbackupHelperPath, args...)

			var outBuffer bytes.Buffer
			helperCmd.Stdout = &outBuffer
			helperCmd.Stderr = &outBuffer

			err = helperCmd.Start()
			Expect(err).ToNot(HaveOccurred())

			for _, i := range []int{1, 3} {
				contents, _ := ioutil.ReadFile(fmt.Sprintf("%s_%d_0", pipeFile, i))
				Expect(string(contents)).To(Equal("here is some data\n"))
			}

			err = helperCmd.Wait()
			printHelperLogOnError(err)
			Expect(err).ToNot(HaveOccurred())

			outputStr := outBuffer.String()
			Expect(outputStr).To(ContainSubstring("Some plugin warning"))

			assertNoErrors()
		})
		It("gpbackup_helper will error out if plugin exits early", func(ctx SpecContext) {
			setupRestoreFiles("", true)

			err := exec.Command("touch", "/tmp/GPBACKUP_PLUGIN_DIE").Run()
			Expect(err).ToNot(HaveOccurred())
			defer exec.Command("rm", "/tmp/GPBACKUP_PLUGIN_DIE").Run()

			args := []string{
				"--toc-file", tocFile,
				"--oid-file", restoreOidFile,
				"--pipe-file", pipeFile,
				"--content", "1",
				"--single-data-file",
				"--restore-agent",
				"--data-file", dataFileFullPath,
				"--plugin-config", examplePluginTestConfig}
			helperCmd := exec.Command(gpbackupHelperPath, args...)

			var outBuffer bytes.Buffer
			helperCmd.Stdout = &outBuffer
			helperCmd.Stderr = &outBuffer

			err = helperCmd.Start()
			Expect(err).ToNot(HaveOccurred())

			contents, _ := ioutil.ReadFile(fmt.Sprintf("%s_%d_0", pipeFile, 1))
			// Empty output
			Expect(contents).To(Equal([]byte{}))

			err = helperCmd.Wait()
			Expect(err).To(HaveOccurred())

			outputStr := outBuffer.String()
			Expect(outputStr).To(ContainSubstring("Plugin process exited with an error"))

			assertErrorsHandled()
		}, SpecTimeout(time.Second*10))
		It("gpbackup_helper will error out if plugin exits early with cluster resize", func(ctx SpecContext) {
			setupRestoreFiles("", true)

			err := exec.Command("touch", "/tmp/GPBACKUP_PLUGIN_DIE").Run()
			Expect(err).ToNot(HaveOccurred())
			defer exec.Command("rm", "/tmp/GPBACKUP_PLUGIN_DIE").Run()

			args := []string{
				"--toc-file", tocFile,
				"--oid-file", restoreOidFile,
				"--pipe-file", pipeFile,
				"--content", "1",
				"--resize-cluster",
				"--orig-seg-count", "6",
				"--dest-seg-count", "3",
				"--restore-agent",
				"--data-file", examplePluginTestDataFile,
				"--plugin-config", examplePluginTestConfig}
			helperCmd := exec.Command(gpbackupHelperPath, args...)

			var outBuffer bytes.Buffer
			helperCmd.Stdout = &outBuffer
			helperCmd.Stderr = &outBuffer

			err = helperCmd.Start()
			Expect(err).ToNot(HaveOccurred())

			contents, _ := ioutil.ReadFile(fmt.Sprintf("%s_%d_0", pipeFile, 1))
			// Empty output
			Expect(contents).To(Equal([]byte{}))

			err = helperCmd.Wait()
			Expect(err).To(HaveOccurred())

			outputStr := outBuffer.String()
			Expect(outputStr).To(ContainSubstring("Plugin process exited with an error"))

			assertErrorsHandled()
		}, SpecTimeout(time.Second*10))
		It("Generates error file when restore agent interrupted", FlakeAttempts(5), func() {
			setupRestoreFiles("gzip", false)
			helperCmd := gpbackupHelperRestore(gpbackupHelperPath, "--data-file", dataFileFullPath+".gz", "--single-data-file")
			waitForPipeCreation()
			err := helperCmd.Process.Signal(unix.SIGINT)
			Expect(err).ToNot(HaveOccurred())
			err = helperCmd.Wait()
			Expect(err).To(HaveOccurred())
			assertErrorsHandled()
		})
		DescribeTable("Skips batches if skip file is discovered with",
			func(oid int, withPlugin bool, args ...string) {
				filesToDelete := setupRestoreWithSkipFiles(oid, withPlugin)
				for _, f := range filesToDelete {
					defer func(filename string) {
						os.Remove(filename)
					}(f)
				}

				args = append([]string{
					"--toc-file", tocFile,
					"--pipe-file", pipeFile,
					"--content", "1",
					"--restore-agent",
					"--oid-file", restoreOidFile,
					"--data-file", dataFileFullPath + ".gz",
					"--on-error-continue",
				}, args...)

				helperCmd := exec.Command(gpbackupHelperPath, args...)
				err := helperCmd.Start()
				Expect(err).ToNot(HaveOccurred())

				// Block here until gpbackup_helper finishes (cleaning up pipes)
				err = helperCmd.Wait()
				Expect(err).ToNot(HaveOccurred())
				for _, i := range []int{1, 2, 3} {
					currentPipe := fmt.Sprintf("%s_%d_0", pipeFile, i)
					Expect(currentPipe).ToNot(BeAnExistingFile())
				}

				By("Check in logs that batches were not restored")

				homeDir := os.Getenv("HOME")
				helperFiles, _ := filepath.Glob(filepath.Join(homeDir, "gpAdminLogs/gpbackup_helper_*"))
				Expect(helperFiles).ToNot(BeEmpty())

				patternHelperPid := fmt.Sprintf(":%06d", helperCmd.Process.Pid)
				helperOut, _ := exec.Command("grep", patternHelperPid, helperFiles[len(helperFiles)-1]).CombinedOutput()
				helperOutput := string(helperOut)

				Expect(helperOutput).ToNot(BeEmpty())

				// Batch 0 should be processed
				Expect(helperOutput).To(ContainSubstring(`: Skip file discovered, skipping this relation`))
				Expect(helperOutput).To(ContainSubstring(`Segment 1: Oid 1, Batch 0: Opening pipe`))

				// Batch 2 must not be processed
				Expect(helperOutput).ToNot(ContainSubstring(`Segment 1: Oid 1, Batch 2: Skip file discovered, skipping this relation`))
				Expect(helperOutput).ToNot(ContainSubstring(`Segment 1: Oid 1, Batch 2: Opening pipe`))
			},
			Entry("single datafile config", -1, false, "--single-data-file"),
			Entry("resize restore", 1, false, "--resize-cluster", "--orig-seg-count", "6", "--dest-seg-count", "3"),
			Entry("single datafile config using a plugin", -1, true, "--single-data-file", "--restore-agent", "--plugin-config", examplePluginTestConfig),
			Entry("resize restore using a plugin", 1, true, "--resize-cluster", "--orig-seg-count", "6", "--dest-seg-count", "3", "--restore-agent", "--plugin-config", examplePluginTestConfig),
		)

		It("Continues restore process when encountering an error with flag --on-error-continue", func() {
			// Write data file
			dataFile := dataFileFullPath
			f, _ := os.Create(dataFile + ".gz")
			gzipf := gzip.NewWriter(f)
			// Named pipes can buffer, so we need to write more than the buffer size to trigger flush error
			customData := "here is some data\n"
			dataLength := 128*1024 + 1
			customData += strings.Repeat("a", dataLength)
			customData += "here is some data\n"

			_, _ = gzipf.Write([]byte(customData))
			_ = gzipf.Close()

			// Write oid file
			fOid, _ := os.Create(restoreOidFile)
			_, _ = fOid.WriteString("1,0\n2,0\n3,0\n")
			defer func() {
				_ = os.Remove(restoreOidFile)
			}()

			err := unix.Mkfifo(fmt.Sprintf("%s_%d_0", pipeFile, 1), 0700)
			if err != nil {
				Fail(fmt.Sprintf("%v", err))
			}

			// Write custom TOC
			customTOC := fmt.Sprintf(`dataentries:
  1:
    startbyte: 0
    endbyte: 18
  2:
    startbyte: 18
    endbyte: %[1]d
  3:
    startbyte: %[1]d
    endbyte: %d
`, dataLength+18, dataLength+18+18)
			fToc, _ := os.Create(tocFile)
			_, _ = fToc.WriteString(customTOC)
			defer func() {
				_ = os.Remove(tocFile)
			}()

			helperCmd := gpbackupHelperRestore(gpbackupHelperPath, "--data-file", dataFileFullPath+".gz", "--on-error-continue")

			for k, v := range []int{1, 2, 3} {
				currentPipe := fmt.Sprintf("%s_%d_0", pipeFile, v)

				if k == 1 {
					// Do not read from the pipe to cause data load error on the helper by interrupting the write.
					file, errOpen := os.Open(currentPipe)
					Expect(errOpen).ToNot(HaveOccurred())
					errClose := file.Close()
					Expect(errClose).ToNot(HaveOccurred())
				} else {
					contents, err := ioutil.ReadFile(currentPipe)
					Expect(err).ToNot(HaveOccurred())
					Expect(string(contents)).To(Equal("here is some data\n"))
				}
			}

			// Block here until gpbackup_helper finishes (cleaning up pipes)
			_ = helperCmd.Wait()
			for _, i := range []int{1, 2, 3} {
				currentPipe := fmt.Sprintf("%s_%d_0", pipeFile, i)
				Expect(currentPipe).ToNot(BeAnExistingFile())
			}

			// Check that an error file was created
			Expect(errorFile).To(BeAnExistingFile())
		})
	})
})

func setupRestoreFiles(compressionType string, withPlugin bool) {
	dataFile := dataFileFullPath
	if withPlugin {
		dataFile = examplePluginTestDataFile
	}

	f, _ := os.Create(restoreOidFile)
	_, _ = f.WriteString("1,0\n3,0\n")

	err := unix.Mkfifo(fmt.Sprintf("%s_%d_0", pipeFile, 1), 0700)
	if err != nil {
		Fail(fmt.Sprintf("%v", err))
	}

	if compressionType == "gzip" {
		f, _ := os.Create(dataFile + ".gz")
		defer f.Close()
		gzipf := gzip.NewWriter(f)
		defer gzipf.Close()
		_, _ = gzipf.Write([]byte(expectedData))
	} else if compressionType == "zstd" {
		f, _ := os.Create(dataFile + ".zst")
		defer f.Close()
		zstdf, _ := zstd.NewWriter(f)
		defer zstdf.Close()
		_, _ = zstdf.Write([]byte(expectedData))
	} else {
		f, _ := os.Create(dataFile)
		_, _ = f.WriteString(expectedData)
	}

	f, _ = os.Create(tocFile)
	_, _ = f.WriteString(expectedTOC)
}

func createDataFile(dataFile string, dataLength int) {
	// Write data file
	f, err := os.Create(dataFile + ".gz")
	if err != nil {
		Fail(fmt.Sprintf("%v", err))
	}
	defer f.Close()
	gzipf := gzip.NewWriter(f)
	defer gzipf.Close()

	// Named pipes can buffer, so we need to write more than the buffer size to trigger flush error
	customData := "here is some data\n"

	customData += strings.Repeat("a", dataLength)
	customData += "here is some data\n"

	if _, err := gzipf.Write([]byte(customData)); err != nil {
		Fail(fmt.Sprintf("%v", err))
	}
}

func createOidFile(fname string, content string) {
	fOid, err := os.Create(fname)
	if err != nil {
		Fail(fmt.Sprintf("Could not create %s: %v", fname, err))
	}
	defer fOid.Close()

	if _, err := fOid.WriteString(content); err != nil {
		Fail(fmt.Sprintf("Could not write to %s: %v", fname, err))
	}
}

func createCustomTOCFile(fname string, dataLength int) {
	customTOC := fmt.Sprintf(`dataentries:
  1:
    startbyte: 0
    endbyte: 18
  2:
    startbyte: 18
    endbyte: %[1]d
  3:
    startbyte: %[1]d
    endbyte: %d
`, dataLength+18, dataLength+18+18)
	fToc, err := os.Create(fname)
	if err != nil {
		Fail(fmt.Sprintf("%v", err))
	}
	defer fToc.Close()

	if _, err = fToc.WriteString(customTOC); err != nil {
		Fail(fmt.Sprintf("%v", err))
	}
}

/*
Tests with skip files and the one with flag --on-error-continue
require a bit more complicated setup, do different setup function.
Returns file name list which must be deleted when done.
*/
func setupRestoreWithSkipFiles(oid int, withPlugin bool) []string {
	dataLength := 128*1024 + 1

	ret := []string{}

	fileName := dataFileFullPath
	if withPlugin {
		fileName = examplePluginTestDataFile
	}
	if oid > 0 {
		fileName = fileName + fmt.Sprintf("_%d", oid)
	}

	createDataFile(fileName, dataLength)
	ret = append(ret, fileName)

	// Write oid file
	createOidFile(restoreOidFile, "1,0\n1,1\n1,2\n")
	ret = append(ret, restoreOidFile)

	pipename := fmt.Sprintf("%s_%d_0", pipeFile, 1)
	err := unix.Mkfifo(pipename, 0700)
	if err != nil {
		Fail(fmt.Sprintf("%v", err))
	}

	createCustomTOCFile(tocFile, dataLength)
	ret = append(ret, tocFile)

	skipFile := utils.GetSkipFilename(pipeFile) + "_1"
	err = exec.Command("touch", skipFile).Run()
	Expect(err).ToNot(HaveOccurred())

	ret = append(ret, skipFile)
	return ret
}

func assertNoErrors() {
	Expect(errorFile).To(Not(BeARegularFile()))
	pipes, err := filepath.Glob(pipeFile + "_[1-9]*")
	Expect(err).ToNot(HaveOccurred())
	Expect(pipes).To(BeEmpty())
}

func assertErrorsHandled() {
	Expect(errorFile).To(BeARegularFile())
	pipes, err := filepath.Glob(pipeFile + "_[1-9]*")
	Expect(err).ToNot(HaveOccurred())
	Expect(pipes).To(BeEmpty())
}

func assertBackupArtifacts(withPlugin bool) {
	var contents []byte
	var err error
	dataFile := dataFileFullPath
	if withPlugin {
		dataFile = examplePluginTestDataFile
	}
	contents, err = ioutil.ReadFile(dataFile)
	Expect(err).ToNot(HaveOccurred())
	Expect(string(contents)).To(Equal(expectedData))

	contents, err = ioutil.ReadFile(tocFile)
	Expect(err).ToNot(HaveOccurred())
	Expect(string(contents)).To(Equal(expectedTOC))
	assertNoErrors()
}

func assertBackupArtifactsWithCompression(compressionType string, withPlugin bool) {
	var contents []byte
	var err error

	dataFile := dataFileFullPath
	if withPlugin {
		dataFile = examplePluginTestDataFile
	}

	if compressionType == "gzip" {
		contents, err = ioutil.ReadFile(dataFile + ".gz")
	} else if compressionType == "zstd" {
		contents, err = ioutil.ReadFile(dataFile + ".zst")
	} else {
		Fail("unknown compression type " + compressionType)
	}
	Expect(err).ToNot(HaveOccurred())

	if compressionType == "gzip" {
		r, _ := gzip.NewReader(bytes.NewReader(contents))
		contents, _ = ioutil.ReadAll(r)
	} else if compressionType == "zstd" {
		r, _ := zstd.NewReader(bytes.NewReader(contents))
		contents, _ = ioutil.ReadAll(r)
	} else {
		Fail("unknown compression type " + compressionType)
	}
	Expect(string(contents)).To(Equal(expectedData))

	contents, err = ioutil.ReadFile(tocFile)
	Expect(err).ToNot(HaveOccurred())
	Expect(string(contents)).To(Equal(expectedTOC))

	assertNoErrors()
}

func printHelperLogOnError(helperErr error) {
	if helperErr != nil {
		homeDir := os.Getenv("HOME")
		helperFiles, _ := filepath.Glob(filepath.Join(homeDir, "gpAdminLogs/gpbackup_helper_*"))
		command := exec.Command("tail", "-n 20", helperFiles[len(helperFiles)-1])
		output, _ := command.CombinedOutput()
		fmt.Println(string(output))
	}
}

func writeToBackupPipes(data string) {
	for i := 1; i <= 3; i++ {
		currentPipe := fmt.Sprintf("%s_%d", pipeFile, i)
		_, err := os.Stat(currentPipe)
		if err != nil {
			Fail(fmt.Sprintf("%v", err))
		}
		f, _ := os.Create("/tmp/tmpdata.txt")
		_, _ = f.WriteString(data)
		output, err := exec.Command("bash", "-c", fmt.Sprintf("cat %s > %s", "/tmp/tmpdata.txt", currentPipe)).CombinedOutput()
		_ = f.Close()
		_ = os.Remove("/tmp/tmpdata.txt")
		if err != nil {
			fmt.Printf("%s", output)
			Fail(fmt.Sprintf("%v", err))
		}
	}
}

func waitForPipeCreation() {
	// wait up to 5 seconds for two pipe files to have been created
	tries := 0
	for tries < 1000 {
		pipes, err := filepath.Glob(pipeFile + "_[1-9]*")
		Expect(err).ToNot(HaveOccurred())
		if len(pipes) > 1 {
			return
		}

		tries += 1
		time.Sleep(5 * time.Millisecond)
	}
}
