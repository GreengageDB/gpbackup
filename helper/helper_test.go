package helper

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/greenplum-db/gpbackup/utils"
	"golang.org/x/sys/unix"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/greenplum-db/gpbackup/toc"
	"github.com/pkg/errors"
)

var (
	testDir     = "/tmp/helper_test/20180101/20180101010101"
	testTocFile = fmt.Sprintf("%s/test_toc.yaml", testDir)
)

type restoreReaderTestImpl struct {
	waitCount      int
	discardedBytes int64
	discardErr     error
}

func (r *restoreReaderTestImpl) waitForPlugin() error {
	r.waitCount++
	return nil
}

func (r *restoreReaderTestImpl) positionReader(pos uint64, oid int) error {
	return nil
}

func (r *restoreReaderTestImpl) copyData(num int64) (int64, error) {
	return 1, nil
}

func (r *restoreReaderTestImpl) copyAllData() (int64, error) {
	return 1, nil
}

func (r *restoreReaderTestImpl) closeFileHandle() {
}

func (r *restoreReaderTestImpl) getReaderType() ReaderType {
	return SUBSET
}

func (r *restoreReaderTestImpl) discardData(num int64) (int64, error) {
	if r.discardErr != nil {
		return 0, r.discardErr
	}

	r.discardedBytes += num
	return num, nil
}

type helperTestStep struct {
	restorePipeWriterArgExpect string
	restorePipeWriterResult    bool
	skipFileArgTableOid        int
	skipFileResult             bool
	comment                    string
}

type restoreMockHelperImpl struct {
	currentStep      int
	started          bool
	expectedOidBatch []oidWithBatch
	expectedSteps    []helperTestStep

	pipesCreated map[string]bool
	pipesOpened  map[string]bool

	restoreData *restoreReaderTestImpl
}

func (h *restoreMockHelperImpl) isPipeOpened(pipe string) bool {
	Expect(h.pipesOpened).ToNot(BeNil())

	return h.pipesOpened[pipe]
}

func (h *restoreMockHelperImpl) isPipeCreated(pipe string) bool {
	Expect(h.pipesCreated).ToNot(BeNil())

	return h.pipesCreated[pipe]
}

func (h *restoreMockHelperImpl) makeStep() helperTestStep {
	if !h.started {
		h.started = true
	} else {
		h.currentStep++
	}

	ret := h.getCurStep()
	fmt.Printf("Step: %s", ret.comment)
	return ret
}

func (h *restoreMockHelperImpl) getCurStep() helperTestStep {
	Expect(h.currentStep).To(BeNumerically("<", len(h.expectedSteps)))
	return h.expectedSteps[h.currentStep]
}

func (h *restoreMockHelperImpl) closeAndDeletePipe(tableOid int, batchNum int) {
	pipename := fmt.Sprintf("%s_%d_%d", *pipeFile, tableOid, batchNum)
	Expect(h.isPipeCreated(pipename)).To(BeTrue())

	delete(h.pipesOpened, pipename)
	delete(h.pipesCreated, pipename)
}

func newHelperTest(batches []oidWithBatch, steps []helperTestStep) *restoreMockHelperImpl {
	var ret = new(restoreMockHelperImpl)
	ret.expectedOidBatch = batches
	ret.expectedSteps = steps
	ret.pipesCreated = make(map[string]bool)
	ret.pipesOpened = make(map[string]bool)
	ret.restoreData = &restoreReaderTestImpl{}

	// pre-create pipes as starter does
	for i := 0; i < *copyQueue; i++ {
		oidBatch := ret.expectedOidBatch[i]
		pipename := fmt.Sprintf("%s_%d_%d", *pipeFile, oidBatch.oid, oidBatch.batch)
		ret.createPipe(pipename)
	}
	return ret
}

func (h *restoreMockHelperImpl) getOidWithBatchListFromFile(oidFileName string) ([]oidWithBatch, error) {
	return h.expectedOidBatch, nil
}

func (h *restoreMockHelperImpl) checkForSkipFile(pipeFile string, tableOid int) bool {
	step := h.getCurStep()
	Expect(tableOid).To(Equal(step.skipFileArgTableOid))
	ret := step.skipFileResult
	return ret
}

func (h *restoreMockHelperImpl) createPipe(pipe string) error {
	// Check that pipe was not opened yet
	Expect(h.isPipeCreated(pipe)).To(Equal(false))
	Expect(h.isPipeOpened(pipe)).To(Equal(false))

	h.pipesCreated[pipe] = true
	return nil
}

func (h *restoreMockHelperImpl) flushAndCloseRestoreWriter(pipeName string, oid int) error {
	// Check that we are closing pipe which is opened
	Expect(h.isPipeOpened(pipeName)).To(Equal(true))
	delete(h.pipesOpened, pipeName)
	return nil
}

func (*restoreMockHelperImpl) doRestoreAgentCleanup() {
	// This was intentionaly left blank to support the IRestoreHelper interface
}

func (h *restoreMockHelperImpl) getRestoreDataReader(fileToRead string, objToc *toc.SegmentTOC, oidList []int) (IRestoreReader, error) {
	Expect(h.restoreData).ToNot(BeNil())
	return h.restoreData, nil
}

func (h *restoreMockHelperImpl) getRestorePipeWriter(currentPipe string) (*bufio.Writer, *os.File, error) {
	step := h.makeStep()
	Expect(currentPipe).To(Equal(step.restorePipeWriterArgExpect))

	// The pipe is opened in getRestorePipeWriter and should not be created before
	Expect(h.isPipeOpened(currentPipe)).To(Equal(false))

	if step.restorePipeWriterResult {
		h.pipesOpened[currentPipe] = true
		var writer bufio.Writer
		return &writer, nil, nil
	}
	return nil, nil, unix.ENXIO
}

type testPluginCmd struct {
	hasProcess_ bool
	error       *string
	waitCount   int
}

func (tp *testPluginCmd) hasProcess() bool {
	return tp.hasProcess_
}

func (pt *testPluginCmd) pid() int {
	return 42
}

func (pt *testPluginCmd) Wait() error {
	pt.waitCount++
	if pt.error == nil {
		return nil
	}
	return errors.New(*pt.error)
}

func (pt *testPluginCmd) errLog() {
}

type limitReader struct {
	remainder int
	err       error
}

func (r *limitReader) Read(p []byte) (n int, err error) {
	if r.remainder <= 0 {
		return 0, r.err
	}

	if len(p) > r.remainder {
		p = p[0:r.remainder]
	}

	n = len(p)
	for i := 0; i < n; i++ {
		p[i] = 1
	}
	r.remainder -= n
	return
}

type limitWriter struct {
	remainder int
}

func (w *limitWriter) Write(p []byte) (n int, err error) {
	if w.remainder < len(p) {
		n = w.remainder
	} else {
		n = len(p)
	}

	if w.remainder == 0 {
		err = io.ErrShortWrite
	}

	w.remainder -= n
	return
}

var _ = Describe("helper tests", func() {
	var pluginConfig utils.PluginConfig
	var isSubset bool
	var fileToRead, fileGzToRead, fileZstdToRead string

	InitializeGlobals()

	*isFiltered = true
	fileToRead = "/tmp/file"
	fileGzToRead = "/tmp/file.gz"
	fileZstdToRead = "/tmp/file.zst"
	pluginConfig = utils.PluginConfig{
		ExecutablePath: "/a/b/myPlugin",
		ConfigPath:     "/tmp/my_plugin_config.yaml",
		Options:        make(map[string]string),
	}

	BeforeEach(func() {
		err := os.MkdirAll(testDir, 0777)
		Expect(err).ShouldNot(HaveOccurred())
	})

	Describe("Check subset flag", func() {
		It("when restore_subset is off, --on-error-continue is false, compression is not used", func() {
			pluginConfig.Options["restore_subset"] = "off"
			*onErrorContinue = false
			isSubset = getSubsetFlag(fileToRead, &pluginConfig)
			Expect(isSubset).To(Equal(false))
		})
		It("when restore_subset is on, --on-error-continue is false, compression is not used", func() {
			pluginConfig.Options["restore_subset"] = "on"
			*onErrorContinue = false
			isSubset = getSubsetFlag(fileToRead, &pluginConfig)
			Expect(isSubset).To(Equal(true))
		})
		It("when restore_subset is off, --on-error-continue is true, compression is not used", func() {
			pluginConfig.Options["restore_subset"] = "off"
			*onErrorContinue = true
			isSubset = getSubsetFlag(fileToRead, &pluginConfig)
			Expect(isSubset).To(Equal(false))
		})
		It("when restore_subset is on, --on-error-continue is true, compression is not used", func() {
			pluginConfig.Options["restore_subset"] = "on"
			*onErrorContinue = true
			isSubset = getSubsetFlag(fileToRead, &pluginConfig)
			Expect(isSubset).To(Equal(true))
		})
		It("when restore_subset is off, --on-error-continue is false, compression \"gz\" is used", func() {
			pluginConfig.Options["restore_subset"] = "off"
			*onErrorContinue = false
			isSubset = getSubsetFlag(fileGzToRead, &pluginConfig)
			Expect(isSubset).To(Equal(false))
		})
		It("when restore_subset is on, --on-error-continue is false, compression \"gz\" is used", func() {
			pluginConfig.Options["restore_subset"] = "on"
			*onErrorContinue = false
			isSubset = getSubsetFlag(fileGzToRead, &pluginConfig)
			Expect(isSubset).To(Equal(false))
		})
		It("when restore_subset is off, --on-error-continue is true, compression \"gz\" is used", func() {
			pluginConfig.Options["restore_subset"] = "off"
			*onErrorContinue = true
			isSubset = getSubsetFlag(fileGzToRead, &pluginConfig)
			Expect(isSubset).To(Equal(false))
		})
		It("when restore_subset is on, --on-error-continue is true, compression \"gz\" is used", func() {
			pluginConfig.Options["restore_subset"] = "on"
			*onErrorContinue = true
			isSubset = getSubsetFlag(fileGzToRead, &pluginConfig)
			Expect(isSubset).To(Equal(false))
		})
		It("when restore_subset is off, --on-error-continue is false, compression \"zstd\" is used", func() {
			pluginConfig.Options["restore_subset"] = "off"
			*onErrorContinue = false
			isSubset = getSubsetFlag(fileZstdToRead, &pluginConfig)
			Expect(isSubset).To(Equal(false))
		})
		It("when restore_subset is on, --on-error-continue is false, compression \"zstd\" is used", func() {
			pluginConfig.Options["restore_subset"] = "on"
			*onErrorContinue = false
			isSubset = getSubsetFlag(fileZstdToRead, &pluginConfig)
			Expect(isSubset).To(Equal(false))
		})
		It("when restore_subset is off, --on-error-continue is true, compression \"zstd\" is used", func() {
			pluginConfig.Options["restore_subset"] = "off"
			*onErrorContinue = true
			isSubset = getSubsetFlag(fileZstdToRead, &pluginConfig)
			Expect(isSubset).To(Equal(false))
		})
		It("when restore_subset is on, --on-error-continue is true, compression \"zstd\" is used", func() {
			pluginConfig.Options["restore_subset"] = "on"
			*onErrorContinue = true
			isSubset = getSubsetFlag(fileZstdToRead, &pluginConfig)
			Expect(isSubset).To(Equal(false))
		})
	})

	Describe("doRestoreAgent Mocked unit tests", func() {
		BeforeEach(func() {
			// Setup mocked tests environment
			*singleDataFile = false
			*content = 1
			*oidFile = "testoid.dat"
			*isResizeRestore = true
			*origSize = 5
			*destSize = 3
			*pipeFile = "mock"
			*onErrorContinue = true
		})
		It("successfully restores using a single data file when inputs are valid and no errors occur", func() {
			*singleDataFile = true

			oidBatch := []oidWithBatch{{oid: 1, batch: 1}}
			steps := []helperTestStep{
				{"mock_1_1", true, 1, false, "Can open single data file"},
			}

			mockHelper := newHelperTest(oidBatch, steps)

			// Prepare and write the toc file
			*tocFile = testTocFile
			writeTestTOC(*tocFile)
			defer func() {
				_ = os.Remove(*tocFile)
			}()

			*dataFile = "test_data.dat"
			// Call the function under test
			err := doRestoreAgentInternal(mockHelper)

			Expect(err).ToNot(HaveOccurred())
		})
		It("successfully restores using multiple data files when inputs are valid and no errors occur", func() {
			// Call the function under test
			oidBatch := []oidWithBatch{{oid: 1, batch: 1}}
			steps := []helperTestStep{
				{"mock_1_1", true, 1, false, "restores using multiple data files"},
			}

			mockHelper := newHelperTest(oidBatch, steps)

			err := doRestoreAgentInternal(mockHelper)

			Expect(err).ToNot(HaveOccurred())
		})
		It("skips batches with corresponding skip file in doRestoreAgent", func() {
			// Test Scenario 1. Simulate 1 pass for the doRestoreAgent() function with the specified oids, batches and expected calls
			oidBatch := []oidWithBatch{
				{100, 0},
				{200, 0},
				{200, 1},
				{200, 2},
			}

			expectedScenario := []helperTestStep{
				{"mock_100_0", true, -1, false, "Can open pipe for table 100, check_skip_file shall not be called"},
				{"mock_200_0", true, -1, false, "Can open pipe for table 200, check_skip_file shall not be called"},
				{"mock_200_1", false, 200, true, "Can not open pipe for table 200, check_skip_file shall called, skip file exists"},
				{"mock_200_2", true, -1, false, "Went to the next batch, Can open pipe for table 200, check_skip_file shall not be called"},
			}

			helper := newHelperTest(oidBatch, expectedScenario)

			err := doRestoreAgentInternal(helper)
			Expect(err).To(BeNil())
		})
		It("skips batches if skip file is discovered with resize restore", func() {
			*origSize = 3
			*destSize = 5

			oidBatch := []oidWithBatch{
				{100, 0},
				{200, 0},
				{200, 1},
				{200, 2},
			}

			expectedScenario := []helperTestStep{
				{"mock_100_0", true, -1, false, "Can open pipe for table 100, check_skip_file shall not be called"},
				{"mock_200_0", true, -1, false, "Can open pipe for table 200, check_skip_file shall not be called"},
				{"mock_200_1", false, 200, true, "Can not open pipe for table 200, check_skip_file shall called, skip file exists"},
				{"mock_200_2", true, -1, false, "Went to the next batch, Can open pipe for table 200, check_skip_file shall not be called"},
			}

			helper := newHelperTest(oidBatch, expectedScenario)
			err := doRestoreAgentInternal(helper)
			Expect(err).To(BeNil())
		})
		It("skips batches if skip file is discovered with single datafile", func() {
			*singleDataFile = true
			*isResizeRestore = false
			*tocFile = testTocFile

			// Although pure concept would be to mock TOC file as well, to keep things simpler
			// let's use real TOC file here
			writeTestTOC(testTocFile)
			defer func() {
				_ = os.Remove(*tocFile)
			}()

			oidBatch := []oidWithBatch{
				{100, 0},
				{200, 0},
				{200, 1},
				{200, 2},
			}

			expectedScenario := []helperTestStep{
				{"mock_100_0", true, -1, false, "Can open pipe for table 100, check_skip_file shall not be called"},
				{"mock_200_0", true, -1, false, "Can open pipe for table 200, check_skip_file shall not be called"},
				{"mock_200_1", false, 200, true, "Can not open pipe for table 200, check_skip_file shall called, skip file exists"},
				{"mock_200_2", true, -1, false, "Went to the next batch, Can open pipe for table 200, check_skip_file shall not be called"},
			}

			helper := newHelperTest(oidBatch, expectedScenario)
			err := doRestoreAgentInternal(helper)
			Expect(err).To(BeNil())
		})
		It("discard data if skip file is discovered with single datafile", func() {
			*singleDataFile = true
			*isResizeRestore = false
			*tocFile = testTocFile

			writeTestTOC(testTocFile)
			defer func() {
				_ = os.Remove(*tocFile)
			}()

			oidBatch := []oidWithBatch{
				{1 /* The first oid from TOC */, 0},
			}

			expectedScenario := []helperTestStep{
				{"mock_1_0", false, 1, true, "Can not open pipe for table 1, check_skip_file shall called, skip file exists"},
			}

			helper := newHelperTest(oidBatch, expectedScenario)
			err := doRestoreAgentInternal(helper)
			Expect(err).ToNot(HaveOccurred())
			Expect(helper.restoreData.discardedBytes).To(Equal(int64(18)))
		})
		It("discard error data if skip file is discovered with single datafile", func() {
			*singleDataFile = true
			*isResizeRestore = false
			*tocFile = testTocFile

			writeTestTOC(testTocFile)
			defer func() {
				_ = os.Remove(*tocFile)
			}()

			oidBatch := []oidWithBatch{
				{1 /* The first oid from TOC */, 0},
			}

			expectedScenario := []helperTestStep{
				{"mock_1_0", false, 1, true, "Can not open pipe for table 1, check_skip_file shall called, skip file exists"},
			}

			helper := newHelperTest(oidBatch, expectedScenario)
			helper.restoreData.discardErr = io.EOF
			err := doRestoreAgentInternal(helper)
			Expect(err).To(Equal(io.EOF))
		})
		It("calls Wait in waitForPlugin doRestoreAgent for single data file", func() {
			*singleDataFile = true
			*isResizeRestore = false
			*tocFile = testTocFile

			// Although pure concept would be to mock TOC file as well, to keep things simpler
			// let's use real TOC file here
			writeTestTOC(testTocFile)
			defer func() {
				_ = os.Remove(*tocFile)
			}()

			oidBatch := []oidWithBatch{{100, 0}}
			expectedScenario := []helperTestStep{
				{"mock_100_0", true, -1, false, "Some pipe shall be created, out of interest for this test although"},
			}
			helper := newHelperTest(oidBatch, expectedScenario)

			err := doRestoreAgentInternal(helper)
			Expect(err).ToNot(HaveOccurred())

			// Check that plugin command's Wait was acually called and only once
			Expect(helper.restoreData.waitCount).To(Equal(1))
		})
		It("calls waitForPlugin doRestoreAgent for resize and no single data file", func() {
			Expect(*singleDataFile).To(Equal(false))

			oidBatch := []oidWithBatch{{100, 0}}
			expectedScenario := []helperTestStep{
				{"mock_100_0", true, -1, false, "Some pipe shall be created, out of interest for this test although"},
			}

			helper := newHelperTest(oidBatch, expectedScenario)

			err := doRestoreAgentInternal(helper)
			Expect(err).ToNot(HaveOccurred())

			// Check that plugin command's Wait was acually called and only once
			Expect(helper.restoreData.waitCount).To(Equal(1))
		})
		It("calls waitForPlugin doRestoreAgent for reduce cluster and no single data file", func() {
			Expect(*singleDataFile).To(Equal(false))
			*origSize = 3
			*destSize = 5

			oidBatch := []oidWithBatch{{100, 0}}
			expectedScenario := []helperTestStep{
				{"mock_100_0", true, -1, false, "Some pipe shall be created, out of interest for this test although"},
			}

			helper := newHelperTest(oidBatch, expectedScenario)

			err := doRestoreAgentInternal(helper)
			Expect(err).ToNot(HaveOccurred())

			// Check that plugin command's Wait was acually called and only once
			Expect(helper.restoreData.waitCount).To(Equal(1))
		})
	})
	Describe("RestoreReader tests", func() {
		AfterEach(func() {
			*onErrorContinue = false
			writer = nil
		})
		It("waitForPlugin normal completion", func() {
			test_cmd1 := testPluginCmd{hasProcess_: true}
			test_reader := new(RestoreReader)
			test_reader.pluginCmd = &test_cmd1

			err := test_reader.waitForPlugin()
			Expect(err).ToNot(HaveOccurred())
			Expect(test_cmd1.waitCount).To(Equal(1))
		})
		It("waitForPlugin do nothing when no cmd and/or no process", func() {
			test_cmd2 := testPluginCmd{hasProcess_: false}
			test_reader := new(RestoreReader)
			test_reader.pluginCmd = &test_cmd2

			err := test_reader.waitForPlugin()
			Expect(err).ToNot(HaveOccurred())
			Expect(test_cmd2.waitCount).To(Equal(0))
		})
		It("waitForPlugin error in Wait happened", func() {
			msg := "Expected test error"
			test_cmd1 := testPluginCmd{hasProcess_: true, error: &msg}
			test_reader := new(RestoreReader)
			test_reader.pluginCmd = &test_cmd1

			err := test_reader.waitForPlugin()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Equal(msg))
		})
		It("CopyData, readerType is SUBSET. Normal completion", func() {
			writer = bufio.NewWriterSize(&limitWriter{100}, 5)

			test_reader := RestoreReader{
				readerType: SUBSET,
				bufReader:  bufio.NewReader(&limitReader{100, io.EOF}),
			}

			bytesRead, err := test_reader.copyData(18)
			Expect(bytesRead).To(Equal(int64(18)))
			Expect(err).ToNot(HaveOccurred())
		})
		It("CopyData, readerType is SUBSET. Error on write", func() {
			*onErrorContinue = true
			bufSize := 5
			toRead := int64(18)
			writer = bufio.NewWriterSize(&limitWriter{7}, bufSize)

			test_reader := RestoreReader{
				readerType: SUBSET,
				bufReader:  bufio.NewReader(&limitReader{100, io.EOF}),
			}

			bytesRead, err := test_reader.copyData(toRead)
			Expect(bytesRead).To(Equal(toRead))
			Expect(errors.Is(err, io.ErrShortWrite)).To(Equal(true))
			str := fmt.Sprintf("copied %d bytes from %d: ", bufSize*2, toRead)
			Expect(strings.HasPrefix(err.Error(), str)).To(Equal(true))

		})
		It("CopyData, readerType is SUBSET. EOF", func() {
			*onErrorContinue = true
			writer = bufio.NewWriterSize(&limitWriter{100}, 5)

			test_reader := RestoreReader{
				readerType: SUBSET,
				bufReader:  bufio.NewReader(&limitReader{25, io.EOF}),
			}

			bytesRead, err := test_reader.copyData(30)
			Expect(bytesRead).To(Equal(int64(25)))
			Expect(err).To(Equal(io.EOF))
		})
		It("CopyData, readerType is SUBSET. Error on write and EOF", func() {
			*onErrorContinue = true
			bufSize := 5
			toCopy := int64(30)
			rLmt := int64(25)
			writer = bufio.NewWriterSize(&limitWriter{7}, bufSize)

			test_reader := RestoreReader{
				readerType: SUBSET,
				bufReader:  bufio.NewReader(&limitReader{int(rLmt), io.EOF}),
			}

			bytesRead, err := test_reader.copyData(toCopy)
			Expect(bytesRead).To(Equal(rLmt))
			Expect(errors.Is(err, io.ErrShortWrite)).To(Equal(true))
			Expect(errors.Is(err, io.EOF)).To(Equal(true))
			readBeforeErr := int64(bufSize * 2)
			prefix := fmt.Sprintf("discard error in copyData: discarded %d bytes from %d: ", rLmt-readBeforeErr, toCopy-readBeforeErr)
			Expect(strings.HasPrefix(err.Error(), prefix)).To(Equal(true))
			strCopied := fmt.Sprintf("copied %d bytes from %d: ", readBeforeErr, toCopy)
			Expect(strings.Contains(err.Error(), strCopied)).To(Equal(true))

			bytesRead, err = test_reader.copyData(10)
			Expect(bytesRead).To(Equal(int64(0)))
			Expect(err.Error()).To(Equal("10 bytes to copy, but discard error has already occurred. Skipping read."))

			bytesRead, err = test_reader.discardData(5)
			Expect(bytesRead).To(Equal(int64(0)))
			Expect(err.Error()).To(Equal("5 bytes to discard, but discard error has already occurred. Skipping read."))
		})
		It("CopyData, readerType is SUBSET. Error on write and on read", func() {
			*onErrorContinue = true
			bufSize := 5
			toCopy := int64(30)
			rLmt := int64(25)
			writer = bufio.NewWriterSize(&limitWriter{7}, bufSize)

			test_reader := RestoreReader{
				readerType: SUBSET,
				bufReader:  bufio.NewReader(&limitReader{int(rLmt), io.ErrNoProgress}),
			}

			bytesRead, err := test_reader.copyData(toCopy)
			Expect(bytesRead).To(Equal(rLmt))
			Expect(errors.Is(err, io.ErrShortWrite)).To(Equal(true))
			Expect(errors.Is(err, io.ErrNoProgress)).To(Equal(true))
			readBeforeErr := int64(bufSize * 2)
			prefix := fmt.Sprintf("discard error in copyData: discarded %d bytes from %d: ", rLmt-readBeforeErr, toCopy-readBeforeErr)
			Expect(strings.HasPrefix(err.Error(), prefix)).To(Equal(true))
			strCopied := fmt.Sprintf("copied %d bytes from %d: ", readBeforeErr, toCopy)
			Expect(strings.Contains(err.Error(), strCopied)).To(Equal(true))

			bytesRead, err = test_reader.copyData(10)
			Expect(bytesRead).To(Equal(int64(0)))
			Expect(err.Error()).To(Equal("10 bytes to copy, but discard error has already occurred. Skipping read."))

			bytesRead, err = test_reader.discardData(5)
			Expect(bytesRead).To(Equal(int64(0)))
			Expect(err.Error()).To(Equal("5 bytes to discard, but discard error has already occurred. Skipping read."))
		})
	})
})

func writeTestTOC(tocFile string) {
	// Write test TOC. We are not going to read data using it, so dataLength is a random number
	dataLength := 100
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
	fToc, err := os.Create(tocFile)
	Expect(err).ShouldNot(HaveOccurred())
	defer fToc.Close()
	fToc.WriteString(customTOC)
}
