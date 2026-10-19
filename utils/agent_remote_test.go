package utils_test

import (
	"fmt"
	"io"
	"os"
	"sync/atomic"

	"github.com/GreengageDB/gp-common-go-libs/cluster"
	"github.com/GreengageDB/gp-common-go-libs/gplog"
	"github.com/GreengageDB/gp-common-go-libs/operating"
	"github.com/GreengageDB/gp-common-go-libs/testhelper"
	"github.com/GreengageDB/gpbackup/filepath"
	"github.com/GreengageDB/gpbackup/utils"
	"github.com/pkg/errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("agent remote", func() {
	var (
		oidList      []string
		fpInfo       filepath.FilePathInfo
		testCluster  *cluster.Cluster
		testExecutor *testhelper.TestExecutor
		remoteOutput *cluster.RemoteOutput
	)
	BeforeEach(func() {
		oidList = []string{"1", "2", "3"}
		operating.System.OpenFileWrite = func(name string, flag int, perm os.FileMode) (io.WriteCloser, error) {
			return buffer, nil
		}

		// Setup test cluster
		coordinatorSeg := cluster.SegConfig{ContentID: -1, Hostname: "localhost", DataDir: "/data/gpseg-1"}
		localSegOne := cluster.SegConfig{ContentID: 0, Hostname: "localhost", DataDir: "/data/gpseg0"}
		remoteSegOne := cluster.SegConfig{ContentID: 1, Hostname: "remotehost1", DataDir: "/data/gpseg1"}

		testExecutor = &testhelper.TestExecutor{}
		remoteOutput = &cluster.RemoteOutput{}
		testExecutor.ClusterOutput = remoteOutput

		testCluster = cluster.NewCluster([]cluster.SegConfig{coordinatorSeg, localSegOne, remoteSegOne})
		testCluster.Executor = testExecutor

		fpInfo = filepath.NewFilePathInfo(testCluster, "", "11112233445566", "", false)
	})
	// note: technically the file system is written to during the call `operating.System.TempFile`
	//			this file is not used throughout the unit tests below, and it is cleaned up with the method: `operating.System.Remove`
	Describe("WriteOidListToSegments()", func() {
		It("generates the correct rsync commands to copy oid file to segments", func() {
			utils.WriteOidListToSegments(oidList, testCluster, fpInfo)

			Expect(testExecutor.NumExecutions).To(Equal(1))
			cc := testExecutor.ClusterCommands[0]
			Expect(len(cc)).To(Equal(2))
			Expect(cc[0].CommandString).To(MatchRegexp("rsync -e ssh .*/gpbackup-oids.* localhost:/data/gpseg0/gpbackup_0_11112233445566_\\d+_oid"))
			Expect(cc[1].CommandString).To(MatchRegexp("rsync -e ssh .*/gpbackup-oids.* remotehost1:/data/gpseg1/gpbackup_1_11112233445566_\\d+_oid"))
		})
		It("panics if any rsync commands fail and outputs correct err messages", func() {
			testExecutor.ErrorOnExecNum = 1
			remoteOutput.NumErrors = 1
			remoteOutput.Scope = cluster.ON_LOCAL & cluster.ON_SEGMENTS
			remoteOutput.Commands = []cluster.ShellCommand{
				cluster.ShellCommand{Content: -1},
				cluster.ShellCommand{Content: 0},
				cluster.ShellCommand{
					Content:       1,
					CommandString: "rsync -e ssh fake_coordinator fake_host",
					Stderr:        "stderr content 1",
					Error:         errors.New("test error 1"),
				},
			}

			Expect(func() { utils.WriteOidListToSegments(oidList, testCluster, fpInfo) }).To(Panic())

			Expect(testExecutor.NumExecutions).To(Equal(1))
			Expect(string(logfile.Contents())).To(ContainSubstring(`[CRITICAL]:-Failed to rsync oid file on 1 segment. See gbytes.Buffer for a complete list of errors.`))
		})
	})
	Describe("CreateSegmentPipesOnAllHostsForBackup()", func() {
		It("creates all the pipes on each segment with a single command", func() {
			utils.CreateSegmentPipesOnAllHostsForBackup(oidList, testCluster, fpInfo, 2)

			Expect(testExecutor.NumExecutions).To(Equal(1))
			cc := testExecutor.ClusterCommands[0]
			Expect(len(cc)).To(Equal(2))
			pipe := fpInfo.GetSegmentPipeFilePath(1, 2)
			Expect(cc[1].CommandString).To(ContainSubstring(fmt.Sprintf(" mkfifo -m 0700 %[1]s_1 %[1]s_2 %[1]s_3", pipe)))
		})
		It("runs no command without pipes to create", func() {
			utils.CreateSegmentPipesOnAllHostsForBackup([]string{}, testCluster, fpInfo)

			Expect(testExecutor.NumExecutions).To(Equal(0))
		})
	})
	Describe("CreateSegmentPipesOnAllHostsForRestore()", func() {
		It("creates all the pipes on each segment with a single command", func() {
			utils.CreateSegmentPipesOnAllHostsForRestore([]string{"1,0", "1,1", "2,0"}, testCluster, fpInfo)

			Expect(testExecutor.NumExecutions).To(Equal(1))
			cc := testExecutor.ClusterCommands[0]
			Expect(len(cc)).To(Equal(2))
			pipe := fpInfo.GetSegmentPipeFilePath(1)
			Expect(cc[1].CommandString).To(ContainSubstring(fmt.Sprintf(" mkfifo %[1]s_1_0 %[1]s_1_1 %[1]s_2_0", pipe)))
		})
	})
	Describe("WriteOidsToFile()", func() {
		It("writes oid list, delimited by newline characters", func() {
			utils.WriteOidsToFile("myFilename", oidList)

			Expect(string(buffer.Contents())).To(Equal("1\n2\n3\n"))
		})
		It("panics and prints when it cannot open local oid file", func() {
			operating.System.OpenFileWrite = func(name string, flag int, perm os.FileMode) (io.WriteCloser, error) {
				return nil, errors.New("cannot open local oid file")
			}

			Expect(func() { utils.WriteOidsToFile("filename", oidList) }).To(Panic())
			Expect(string(logfile.Contents())).To(ContainSubstring(`cannot open local oid file`))
		})
		It("panics and prints when it cannot close local oid file", func() {
			operating.System.OpenFileWrite = func(name string, flag int, perm os.FileMode) (io.WriteCloser, error) {
				return testWriter{CloseErr: errors.New("cannot close local oid file")}, nil
			}

			Expect(func() { utils.WriteOidsToFile("filename", oidList) }).To(Panic())
			Expect(string(logfile.Contents())).To(ContainSubstring(`cannot close local oid file`))
		})
		It("panics and prints when WriteOids returns an error", func() {
			operating.System.OpenFileWrite = func(name string, flag int, perm os.FileMode) (io.WriteCloser, error) {
				return testWriter{WriteErr: errors.New("WriteOids returned err")}, nil
			}

			Expect(func() { utils.WriteOidsToFile("filename", oidList) }).To(Panic())
			Expect(string(logfile.Contents())).To(ContainSubstring("WriteOids returned err"))
		})
	})
	Describe("WriteOids()", func() {
		It("writes oid list, delimited by newline characters", func() {
			err := utils.WriteOids(buffer, oidList)
			Expect(err).ToNot(HaveOccurred())

			Expect(string(buffer.Contents())).To(Equal("1\n2\n3\n"))
		})
		It("returns an error if it fails to write an oid", func() {
			tw := testWriter{}
			tw.WriteErr = errors.New("fail oid write")

			err := utils.WriteOids(tw, oidList)
			Expect(err).To(Equal(tw.WriteErr))
		})
	})
	Describe("LockHelperStart()", func() {
		It("keeps another start or the cleanup of the helpers waiting until it is unlocked", func() {
			var wasTerminated atomic.Bool
			unlock := utils.LockHelperStart(&wasTerminated)
			locked := make(chan bool)
			go func() {
				utils.LockHelperStart(&wasTerminated)()
				close(locked)
			}()
			Consistently(locked, "200ms").ShouldNot(BeClosed())
			unlock()
			Eventually(locked).Should(BeClosed())
		})
	})
	Describe("StartGpbackupHelpers()", func() {
		It("Correctly propagates --on-error-continue flag to gpbackup_helper", func() {
			utils.StartGpbackupHelpers(testCluster, fpInfo, "operation", "/tmp/pluginConfigFile.yml", " compressStr", true, false, 1, true, false, 0, 0, 0, gplog.LOGINFO)

			cc := testExecutor.ClusterCommands[0]
			Expect(cc[1].CommandString).To(ContainSubstring(" --on-error-continue"))
		})
		It("Correctly propagates --copy-queue-size value to gpbackup_helper", func() {
			utils.StartGpbackupHelpers(testCluster, fpInfo, "operation", "/tmp/pluginConfigFile.yml", " compressStr", false, false, 4, true, false, 0, 0, 0, gplog.LOGINFO)

			cc := testExecutor.ClusterCommands[0]
			Expect(cc[1].CommandString).To(ContainSubstring(" --copy-queue-size 4"))
		})
		It("Correctly propagates verbosity", func() {
			verbosity := gplog.LOGDEBUG
			utils.StartGpbackupHelpers(testCluster, fpInfo, "operation", "/tmp/pluginConfigFile.yml", " compressStr", false, false, 4, true, false, 0, 0, 0, verbosity)

			cc := testExecutor.ClusterCommands[0]
			Expect(cc[1].CommandString).To(ContainSubstring("--verbosity %d", gplog.LOGDEBUG))
		})
		It("Names the files of the gpbackup_helper of a data file other than the first by its index", func() {
			utils.StartGpbackupHelpers(testCluster, fpInfo, "--restore-agent", "/tmp/pluginConfigFile.yml", " compressStr", false, false, 4, true, false, 0, 0, 1, gplog.LOGINFO, 1)

			cc := testExecutor.ClusterCommands[0]
			Expect(cc[1].CommandString).To(MatchRegexp(`--toc-file /data/gpseg1/backups/11112233/11112233445566/gpbackup_1_11112233445566_toc1.yaml --oid-file /data/gpseg1/gpbackup_1_11112233445566_\d+_oid1 --pipe-file /data/gpseg1/gpbackup_1_11112233445566_\d+_pipe1 --data-file "/data/gpseg1/backups/11112233/11112233445566/gpbackup_1_11112233445566_file1"`))
		})
		It("Gives no oid file to a gpbackup_helper that backs up", func() {
			utils.StartGpbackupHelpers(testCluster, fpInfo, "--backup-agent", "/tmp/pluginConfigFile.yml", " compressStr", false, false, 4, true, false, 0, 0, 1, gplog.LOGINFO, 1)

			cc := testExecutor.ClusterCommands[0]
			Expect(cc[1].CommandString).To(MatchRegexp(`--toc-file /data/gpseg1/backups/11112233/11112233445566/gpbackup_1_11112233445566_toc1.yaml --pipe-file /data/gpseg1/gpbackup_1_11112233445566_\d+_pipe1 `))
			Expect(cc[1].CommandString).ToNot(ContainSubstring("--oid-file"))
		})
	})
	Describe("FinishSlotBackupHelpers()", func() {
		// The commands for the next slot and for the segment TOC of one data file
		wakeCommand := func(nextSlot string, tocFile string) string {
			return fmt.Sprintf(`(while [[ -p "%[1]s" ]]; do timeout 10 bash -c '[[ ! -p "%[1]s" ]] || : > "%[1]s"' && break; `+
				`ps ux | grep -F -- "--toc-file %[2]s " | grep -- "[g]pbackup_helper" | grep -q -- "--backup-agent" || exit 1; sleep 1; done)`, nextSlot, tocFile)
		}
		waitCommand := func(tocFile string, errorFile string) string {
			return fmt.Sprintf(`(while [[ ! -f "%[1]s" && ! -f "%[2]s" ]]; do `+
				`ps ux | grep -F -- "--toc-file %[1]s " | grep -- "[g]pbackup_helper" | grep -q -- "--backup-agent" || break; sleep 1; done; ls "%[1]s")`, tocFile, errorFile)
		}
		It("ends the next slot of a single data file, whose files have no index, and waits for its segment TOC", func() {
			utils.FinishSlotBackupHelpers(testCluster, fpInfo, []int{3})

			Expect(testExecutor.NumExecutions).To(Equal(1))
			finishCommand := testExecutor.ClusterCommands[0][0].CommandString
			pipeFile := fpInfo.GetSegmentPipeFilePath(0)
			tocFile := "/data/gpseg0/backups/11112233/11112233445566/gpbackup_0_11112233445566_toc.yaml"
			Expect(finishCommand).To(ContainSubstring(wakeCommand(pipeFile+"_3", tocFile) + " && " + waitCommand(tocFile, utils.GetErrorFilename(pipeFile))))
		})
		It("ends the next slot of each data file and waits for its segment TOC", func() {
			utils.FinishSlotBackupHelpers(testCluster, fpInfo, []int{2, 1})

			Expect(testExecutor.NumExecutions).To(Equal(1))
			finishCommand := testExecutor.ClusterCommands[0][0].CommandString
			pipeFile := func(dataFile int) string { return fpInfo.GetSegmentPipeFilePath(0, dataFile) }
			tocFile := func(dataFile int) string { return fpInfo.GetSegmentTOCFilePathForDataFile(0, dataFile) }
			Expect(finishCommand).To(ContainSubstring(wakeCommand(pipeFile(0)+"_2", tocFile(0)) + " && " +
				wakeCommand(pipeFile(1)+"_1", tocFile(1)) + " && " +
				waitCommand(tocFile(0), utils.GetErrorFilename(pipeFile(0))) + " && " +
				waitCommand(tocFile(1), utils.GetErrorFilename(pipeFile(1)))))
			Expect(tocFile(0)).To(HaveSuffix("_toc.yaml"))
			Expect(tocFile(1)).To(HaveSuffix("_toc1.yaml"))
		})
	})
	Describe("CreateSkipFilesOnSegments()", func() {
		It("creates the skip files of the entries a thousand at a time on each segment", func() {
			oids := make([]uint32, 1500)
			for i := range oids {
				oids[i] = uint32(i + 1)
			}
			utils.CreateSkipFilesOnSegments(oids, testCluster, fpInfo, 2)

			Expect(testExecutor.NumExecutions).To(Equal(2))
			skipFile := utils.GetSkipFilename(fpInfo.GetSegmentPipeFilePath(0, 2))
			first := testExecutor.ClusterCommands[0][0].CommandString
			Expect(first).To(ContainSubstring(fmt.Sprintf("touch %[1]s_1 %[1]s_2 ", skipFile)))
			Expect(first).To(ContainSubstring(fmt.Sprintf(" %s_1000", skipFile)))
			Expect(first).ToNot(ContainSubstring(fmt.Sprintf(" %s_1001", skipFile)))
			second := testExecutor.ClusterCommands[1][0].CommandString
			Expect(second).To(ContainSubstring(fmt.Sprintf("touch %s_1001 ", skipFile)))
			Expect(second).To(HaveSuffix(fmt.Sprintf(" %s_1500", skipFile)))
		})
	})
	Describe("CheckAgentErrorsOnSegments", func() {
		It("constructs the correct ssh call to check for the existance of an error file on each segment", func() {
			err := utils.CheckAgentErrorsOnSegments(testCluster, fpInfo)
			Expect(err).ToNot(HaveOccurred())

			cc := testExecutor.ClusterCommands[0]
			errorFile0 := fmt.Sprintf(`/data/gpseg0/gpbackup_0_11112233445566_%d_error`, fpInfo.PID)
			expectedCmd0 := fmt.Sprintf(`if ls %[1]s* >/dev/null 2>/dev/null; then echo 'error'; fi; rm -f %[1]s*`, errorFile0)
			Expect(cc[0].CommandString).To(ContainSubstring(expectedCmd0))

			errorFile1 := fmt.Sprintf(`/data/gpseg1/gpbackup_1_11112233445566_%d_error`, fpInfo.PID)
			expectedCmd1 := fmt.Sprintf(`if ls %[1]s* >/dev/null 2>/dev/null; then echo 'error'; fi; rm -f %[1]s*`, errorFile1)
			Expect(cc[1].CommandString).To(ContainSubstring(expectedCmd1))
		})
		It("leaves the error file of a helper that other connections restore from", func() {
			testExecutor.ClusterOutput = &cluster.RemoteOutput{Commands: []cluster.ShellCommand{{Content: 0, Stdout: "error"}, {Content: 1}}}
			err := utils.CheckAgentErrorsOnSegmentsKeepingErrorFile(testCluster, fpInfo, 1)
			Expect(errors.Is(err, utils.AgentErr)).To(BeTrue())

			cc := testExecutor.ClusterCommands[0]
			errorFile0 := fmt.Sprintf(`/data/gpseg0/gpbackup_0_11112233445566_%d_error1`, fpInfo.PID)
			Expect(cc[0].CommandString).To(HaveSuffix(fmt.Sprintf(`if ls %s >/dev/null 2>/dev/null; then echo 'error'; fi`, errorFile0)))
		})

	})
})

type testWriter struct {
	WriteErr error
	CloseErr error
}

func (f testWriter) Write(p []byte) (n int, err error) {
	_ = p
	return 0, f.WriteErr
}
func (f testWriter) Close() error {
	return f.CloseErr
}
