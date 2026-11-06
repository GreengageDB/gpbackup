package end_to_end_test

import (
	"bufio"
	"math/rand"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/GreengageDB/gp-common-go-libs/testhelper"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/sys/unix"
)

func createCombinedOutput(cmd *exec.Cmd) *bufio.Scanner {
	outPipe, err := cmd.StdoutPipe()
	Expect(err).ToNot(HaveOccurred())
	outScanner := bufio.NewScanner(outPipe)
	cmd.Stderr = cmd.Stdout
	return outScanner
}

func waitOutputLine(scanner *bufio.Scanner, output *strings.Builder) {
	if !scanner.Scan() || scanner.Err() != nil {
		Fail("no output from process")
	}
	output.WriteString(scanner.Text())
	output.WriteByte('\n')
}

func readOutput(scanner *bufio.Scanner, output *strings.Builder) {
	for scanner.Scan() {
		output.WriteString(scanner.Text())
		output.WriteByte('\n')
	}
}

var _ = Describe("Signal handler tests", func() {
	BeforeEach(func() {
		end_to_end_setup()
		testhelper.AssertQueryRuns(backupConn, "CREATE table bigtable(id int unique); INSERT INTO bigtable SELECT generate_series(1,10000000)")
	})
	AfterEach(func() {
		end_to_end_teardown()
		testhelper.AssertQueryRuns(backupConn, "DROP TABLE bigtable")
	})
	Context("SIGINT", func() {
		It("runs gpbackup and sends a SIGINT to ensure cleanup functions successfully", func() {
			if useOldBackupVersion {
				Skip("This test is not needed for old backup versions")
			}
			args := []string{"--dbname", "testdb",
				"--backup-dir", backupDir,
				"--single-data-file",
				"--verbose"}
			cmd := exec.Command(gpbackupPath, args...)
			outScanner := createCombinedOutput(cmd)
			var output strings.Builder
			var wg sync.WaitGroup
			wg.Add(1)

			go func() {
				defer wg.Done()
				waitOutputLine(outScanner, &output)

				/*
				* We use a random delay for the sleep in this test (between
				* 0.5s and 1.5s) so that gpbackup will be interrupted at a
				* different point in the backup process every time to help
				* catch timing issues with the cleanup.
				 */
				rng := rand.New(rand.NewSource(time.Now().UnixNano()))
				time.Sleep(time.Duration(rng.Intn(1000)+500) * time.Millisecond)
				_ = cmd.Process.Signal(unix.SIGINT)

				readOutput(outScanner, &output)
			}()
			err := cmd.Start()
			Expect(err).ToNot(HaveOccurred())
			wg.Wait()
			stdout := output.String()
			Expect(stdout).To(ContainSubstring("Received an interrupt signal, aborting backup process"))
			Expect(stdout).To(ContainSubstring("Cleanup complete"))
			Expect(stdout).To(Not(ContainSubstring("CRITICAL")))
			timestamp := getBackupTimestamp(stdout)
			if timestamp != "" { // empty timestamp means backup was killed before generating timestamp
				assertArtifactsCleaned(timestamp)
			}
		})
		It("runs gpbackup with copy-queue-size and sends a SIGINT to ensure cleanup functions successfully", func() {
			if useOldBackupVersion {
				Skip("This test is not needed for old backup versions")
			}
			args := []string{"--dbname", "testdb",
				"--backup-dir", backupDir,
				"--single-data-file",
				"--copy-queue-size", "4",
				"--verbose"}
			cmd := exec.Command(gpbackupPath, args...)
			outScanner := createCombinedOutput(cmd)
			var output strings.Builder
			var wg sync.WaitGroup
			wg.Add(1)

			go func() {
				defer wg.Done()
				waitOutputLine(outScanner, &output)

				/*
				* We use a random delay for the sleep in this test (between
				* 0.5s and 1.5s) so that gpbackup will be interrupted at a
				* different point in the backup process every time to help
				* catch timing issues with the cleanup.
				 */
				rng := rand.New(rand.NewSource(time.Now().UnixNano()))
				time.Sleep(time.Duration(rng.Intn(1000)+500) * time.Millisecond)
				_ = cmd.Process.Signal(unix.SIGINT)

				readOutput(outScanner, &output)
			}()
			err := cmd.Start()
			Expect(err).ToNot(HaveOccurred())
			wg.Wait()
			stdout := output.String()
			Expect(stdout).To(ContainSubstring("Received an interrupt signal, aborting backup process"))
			Expect(stdout).To(ContainSubstring("Cleanup complete"))
			Expect(stdout).To(Not(ContainSubstring("CRITICAL")))
			timestamp := getBackupTimestamp(stdout)
			if timestamp != "" { // empty timestamp means backup was killed before generating timestamp
				assertArtifactsCleaned(timestamp)
			}
		})
		It("runs gpbackup and sends a SIGINT to ensure blocked LOCK TABLE query is canceled", func() {
			if useOldBackupVersion {
				Skip("This test is not needed for old backup versions")
			}

			// Query to see if gpbackup lock acquire on schema2.foo2 is blocked
			checkLockQuery := `SELECT count(*) FROM pg_locks l, pg_class c, pg_namespace n WHERE l.relation = c.oid AND n.oid = c.relnamespace AND n.nspname = 'schema2' AND c.relname = 'foo2' AND l.granted = 'f'`

			// Acquire AccessExclusiveLock on schema2.foo2 to prevent gpbackup from acquiring AccessShareLock
			backupConn.MustBegin()
			backupConn.MustExec("LOCK TABLE schema2.foo2 IN ACCESS EXCLUSIVE MODE")
			args := []string{
				"--dbname", "testdb",
				"--backup-dir", backupDir,
				"--verbose"}
			cmd := exec.Command(gpbackupPath, args...)
			outScanner := createCombinedOutput(cmd)
			var output strings.Builder
			var wg sync.WaitGroup
			wg.Add(1)

			// Wait up to 5 seconds for gpbackup to block on acquiring AccessShareLock.
			// Once blocked, we send a SIGINT to cancel gpbackup.
			var beforeLockCount int
			go func() {
				defer wg.Done()
				waitOutputLine(outScanner, &output)

				iterations := 50
				for iterations > 0 {
					_ = backupConn.Get(&beforeLockCount, checkLockQuery)
					if beforeLockCount < 1 {
						time.Sleep(100 * time.Millisecond)
						iterations--
					} else {
						break
					}
				}
				_ = cmd.Process.Signal(unix.SIGINT)

				readOutput(outScanner, &output)
			}()
			err := cmd.Start()
			Expect(err).ToNot(HaveOccurred())
			wg.Wait()
			Expect(beforeLockCount).To(Equal(1))

			// After gpbackup has been canceled, we should no longer see a blocked SQL
			// session trying to acquire AccessShareLock on foo2.
			var afterLockCount int
			_ = backupConn.Get(&afterLockCount, checkLockQuery)
			Expect(afterLockCount).To(Equal(0))
			backupConn.MustRollback()

			stdout := output.String()
			Expect(stdout).To(ContainSubstring("Received an interrupt signal, aborting backup process"))
			Expect(stdout).To(ContainSubstring("Interrupt received while acquiring ACCESS SHARE locks on tables"))
			Expect(stdout).To(ContainSubstring("Cleanup complete"))
			Expect(stdout).To(Not(ContainSubstring("CRITICAL")))
			timestamp := getBackupTimestamp(stdout)
			assertArtifactsCleaned(timestamp)
		})
		It("runs gpbackup with single-data-file and sends a SIGINT to ensure blocked LOCK TABLE query is canceled", func() {
			if useOldBackupVersion {
				Skip("This test is not needed for old backup versions")
			}

			// Query to see if gpbackup lock acquire on schema2.foo2 is blocked
			checkLockQuery := `SELECT count(*) FROM pg_locks l, pg_class c, pg_namespace n WHERE l.relation = c.oid AND n.oid = c.relnamespace AND n.nspname = 'schema2' AND c.relname = 'foo2' AND l.granted = 'f'`

			// Acquire AccessExclusiveLock on schema2.foo2 to prevent gpbackup from acquiring AccessShareLock
			backupConn.MustBegin()
			backupConn.MustExec("LOCK TABLE schema2.foo2 IN ACCESS EXCLUSIVE MODE")
			args := []string{
				"--dbname", "testdb",
				"--backup-dir", backupDir,
				"--single-data-file",
				"--verbose"}
			cmd := exec.Command(gpbackupPath, args...)
			outScanner := createCombinedOutput(cmd)
			var output strings.Builder
			var wg sync.WaitGroup
			wg.Add(1)

			// Wait up to 5 seconds for gpbackup to block on acquiring AccessShareLock.
			// Once blocked, we send a SIGINT to cancel gpbackup.
			var beforeLockCount int
			go func() {
				defer wg.Done()
				waitOutputLine(outScanner, &output)

				iterations := 50
				for iterations > 0 {
					_ = backupConn.Get(&beforeLockCount, checkLockQuery)
					if beforeLockCount < 1 {
						time.Sleep(100 * time.Millisecond)
						iterations--
					} else {
						break
					}
				}
				_ = cmd.Process.Signal(unix.SIGINT)

				readOutput(outScanner, &output)
			}()
			err := cmd.Start()
			Expect(err).ToNot(HaveOccurred())
			wg.Wait()
			Expect(beforeLockCount).To(Equal(1))

			// After gpbackup has been canceled, we should no longer see a blocked SQL
			// session trying to acquire AccessShareLock on foo2.
			var afterLockCount int
			_ = backupConn.Get(&afterLockCount, checkLockQuery)
			Expect(afterLockCount).To(Equal(0))
			backupConn.MustRollback()

			stdout := output.String()
			Expect(stdout).To(ContainSubstring("Received an interrupt signal, aborting backup process"))
			Expect(stdout).To(ContainSubstring("Interrupt received while acquiring ACCESS SHARE locks on tables"))
			Expect(stdout).To(ContainSubstring("Cleanup complete"))
			Expect(stdout).To(Not(ContainSubstring("CRITICAL")))
			timestamp := getBackupTimestamp(stdout)
			assertArtifactsCleaned(timestamp)
		})
		It("runs gprestore and sends a SIGINT to ensure cleanup functions successfully", func() {
			if useOldBackupVersion {
				Skip("This test is not needed for old backup versions")
			}
			backupOutput := gpbackup(gpbackupPath, backupHelperPath,
				"--backup-dir", backupDir,
				"--single-data-file")
			timestamp := getBackupTimestamp(string(backupOutput))
			args := []string{
				"--timestamp", timestamp,
				"--redirect-db", "restoredb",
				"--backup-dir", backupDir,
				"--verbose"}
			cmd := exec.Command(gprestorePath, args...)
			outScanner := createCombinedOutput(cmd)
			var output strings.Builder
			var wg sync.WaitGroup
			wg.Add(1)

			go func() {
				defer wg.Done()
				waitOutputLine(outScanner, &output)

				/*
				* We use a random delay for the sleep in this test (between
				* 0.5s and 1.5s) so that gprestore will be interrupted at a
				* different point in the backup process every time to help
				* catch timing issues with the cleanup.
				 */
				rng := rand.New(rand.NewSource(time.Now().UnixNano()))
				time.Sleep(time.Duration(rng.Intn(1000)+500) * time.Millisecond)
				_ = cmd.Process.Signal(unix.SIGINT)

				readOutput(outScanner, &output)
			}()
			err := cmd.Start()
			Expect(err).ToNot(HaveOccurred())
			wg.Wait()
			stdout := output.String()
			Expect(stdout).To(ContainSubstring("Received an interrupt signal, aborting restore process"))
			Expect(stdout).To(ContainSubstring("Cleanup complete"))
			Expect(stdout).To(Not(ContainSubstring("CRITICAL")))
			assertArtifactsCleaned(timestamp)
		})
		It("runs gprestore with copy-queue-size and sends a SIGINT to ensure cleanup functions successfully", func() {
			if useOldBackupVersion {
				Skip("This test is not needed for old backup versions")
			}
			outputBkp := gpbackup(gpbackupPath, backupHelperPath,
				"--backup-dir", backupDir,
				"--single-data-file")
			timestampBkp := getBackupTimestamp(string(outputBkp))
			args := []string{
				"--timestamp", timestampBkp,
				"--redirect-db", "restoredb",
				"--backup-dir", backupDir,
				"--verbose",
				"--copy-queue-size", "4"}
			cmd := exec.Command(gprestorePath, args...)
			outScanner := createCombinedOutput(cmd)
			var output strings.Builder
			var wg sync.WaitGroup
			wg.Add(1)

			go func() {
				defer wg.Done()
				waitOutputLine(outScanner, &output)

				/*
				* We use a random delay for the sleep in this test (between
				* 0.5s and 1.5s) so that gprestore will be interrupted at a
				* different point in the backup process every time to help
				* catch timing issues with the cleanup.
				 */
				rng := rand.New(rand.NewSource(time.Now().UnixNano()))
				time.Sleep(time.Duration(rng.Intn(1000)+500) * time.Millisecond)
				_ = cmd.Process.Signal(unix.SIGINT)

				readOutput(outScanner, &output)
			}()
			err := cmd.Start()
			Expect(err).ToNot(HaveOccurred())
			wg.Wait()
			stdoutRes := output.String()
			Expect(stdoutRes).To(ContainSubstring("Received an interrupt signal, aborting restore process"))
			Expect(stdoutRes).To(ContainSubstring("Cleanup complete"))
			Expect(stdoutRes).To(Not(ContainSubstring("CRITICAL")))
			assertArtifactsCleaned(timestampBkp)
		})
	})
	Context("SIGTERM", func() {
		It("runs gpbackup and sends a SIGTERM to ensure cleanup functions successfully", func() {
			if useOldBackupVersion {
				Skip("This test is not needed for old backup versions")
			}
			args := []string{"--dbname", "testdb",
				"--backup-dir", backupDir,
				"--single-data-file",
				"--verbose"}
			cmd := exec.Command(gpbackupPath, args...)
			outScanner := createCombinedOutput(cmd)
			var output strings.Builder
			var wg sync.WaitGroup
			wg.Add(1)

			go func() {
				defer wg.Done()
				waitOutputLine(outScanner, &output)

				/*
				* We use a random delay for the sleep in this test (between
				* 0.5s and 1.5s) so that gpbackup will be interrupted at a
				* different point in the backup process every time to help
				* catch timing issues with the cleanup.
				 */
				rng := rand.New(rand.NewSource(time.Now().UnixNano()))
				time.Sleep(time.Duration(rng.Intn(1000)+500) * time.Millisecond)
				_ = cmd.Process.Signal(unix.SIGTERM)

				readOutput(outScanner, &output)
			}()
			err := cmd.Start()
			Expect(err).ToNot(HaveOccurred())
			wg.Wait()
			stdout := output.String()
			Expect(stdout).To(ContainSubstring("Received a termination signal, aborting backup process"))
			Expect(stdout).To(ContainSubstring("Cleanup complete"))
			Expect(stdout).To(Not(ContainSubstring("CRITICAL")))
			timestamp := getBackupTimestamp(stdout)
			if timestamp != "" { // empty timestamp means backup was killed before generating timestamp
				assertArtifactsCleaned(timestamp)
			}
		})
		It("runs gpbackup with copy-queue-size and sends a SIGTERM to ensure cleanup functions successfully", func() {
			if useOldBackupVersion {
				Skip("This test is not needed for old backup versions")
			}
			args := []string{"--dbname", "testdb",
				"--backup-dir", backupDir,
				"--single-data-file",
				"--copy-queue-size", "4",
				"--verbose"}
			cmd := exec.Command(gpbackupPath, args...)
			outScanner := createCombinedOutput(cmd)
			var output strings.Builder
			var wg sync.WaitGroup
			wg.Add(1)

			go func() {
				defer wg.Done()
				waitOutputLine(outScanner, &output)

				/*
				* We use a random delay for the sleep in this test (between
				* 0.5s and 1.5s) so that gpbackup will be interrupted at a
				* different point in the backup process every time to help
				* catch timing issues with the cleanup.
				 */
				rng := rand.New(rand.NewSource(time.Now().UnixNano()))
				time.Sleep(time.Duration(rng.Intn(1000)+500) * time.Millisecond)
				_ = cmd.Process.Signal(unix.SIGTERM)

				readOutput(outScanner, &output)
			}()
			err := cmd.Start()
			Expect(err).ToNot(HaveOccurred())
			wg.Wait()
			stdout := output.String()
			Expect(stdout).To(ContainSubstring("Received a termination signal, aborting backup process"))
			Expect(stdout).To(ContainSubstring("Cleanup complete"))
			Expect(stdout).To(Not(ContainSubstring("CRITICAL")))
			timestamp := getBackupTimestamp(stdout)
			if timestamp != "" { // empty timestamp means backup was killed before generating timestamp
				assertArtifactsCleaned(timestamp)
			}
		})
		It("runs gpbackup and sends a SIGTERM to ensure blocked LOCK TABLE query is canceled", func() {
			if useOldBackupVersion {
				Skip("This test is not needed for old backup versions")
			}

			// Query to see if gpbackup lock acquire on schema2.foo2 is blocked
			checkLockQuery := `SELECT count(*) FROM pg_locks l, pg_class c, pg_namespace n WHERE l.relation = c.oid AND n.oid = c.relnamespace AND n.nspname = 'schema2' AND c.relname = 'foo2' AND l.granted = 'f'`

			// Acquire AccessExclusiveLock on schema2.foo2 to prevent gpbackup from acquiring AccessShareLock
			backupConn.MustBegin()
			backupConn.MustExec("LOCK TABLE schema2.foo2 IN ACCESS EXCLUSIVE MODE")
			args := []string{
				"--dbname", "testdb",
				"--backup-dir", backupDir,
				"--verbose"}
			cmd := exec.Command(gpbackupPath, args...)
			outScanner := createCombinedOutput(cmd)
			var output strings.Builder
			var wg sync.WaitGroup
			wg.Add(1)

			// Wait up to 5 seconds for gpbackup to block on acquiring AccessShareLock.
			// Once blocked, we send a SIGTERM to cancel gpbackup.
			var beforeLockCount int
			go func() {
				defer wg.Done()
				waitOutputLine(outScanner, &output)

				iterations := 50
				for iterations > 0 {
					_ = backupConn.Get(&beforeLockCount, checkLockQuery)
					if beforeLockCount < 1 {
						time.Sleep(100 * time.Millisecond)
						iterations--
					} else {
						break
					}
				}
				_ = cmd.Process.Signal(unix.SIGTERM)

				readOutput(outScanner, &output)
			}()
			err := cmd.Start()
			Expect(err).ToNot(HaveOccurred())
			wg.Wait()
			Expect(beforeLockCount).To(Equal(1))

			// After gpbackup has been canceled, we should no longer see a blocked SQL
			// session trying to acquire AccessShareLock on foo2.
			var afterLockCount int
			_ = backupConn.Get(&afterLockCount, checkLockQuery)
			Expect(afterLockCount).To(Equal(0))
			backupConn.MustRollback()

			stdout := output.String()
			Expect(stdout).To(ContainSubstring("Received a termination signal, aborting backup process"))
			Expect(stdout).To(ContainSubstring("Interrupt received while acquiring ACCESS SHARE locks on tables"))
			Expect(stdout).To(ContainSubstring("Cleanup complete"))
			Expect(stdout).To(Not(ContainSubstring("CRITICAL")))
			timestamp := getBackupTimestamp(stdout)
			assertArtifactsCleaned(timestamp)
		})
		It("runs gpbackup with single-data-file and sends a SIGTERM to ensure blocked LOCK TABLE query is canceled", func() {
			if useOldBackupVersion {
				Skip("This test is not needed for old backup versions")
			}

			// Query to see if gpbackup lock acquire on schema2.foo2 is blocked
			checkLockQuery := `SELECT count(*) FROM pg_locks l, pg_class c, pg_namespace n WHERE l.relation = c.oid AND n.oid = c.relnamespace AND n.nspname = 'schema2' AND c.relname = 'foo2' AND l.granted = 'f'`

			// Acquire AccessExclusiveLock on schema2.foo2 to prevent gpbackup from acquiring AccessShareLock
			backupConn.MustBegin()
			backupConn.MustExec("LOCK TABLE schema2.foo2 IN ACCESS EXCLUSIVE MODE")
			args := []string{
				"--dbname", "testdb",
				"--backup-dir", backupDir,
				"--single-data-file",
				"--verbose"}
			cmd := exec.Command(gpbackupPath, args...)
			outScanner := createCombinedOutput(cmd)
			var output strings.Builder
			var wg sync.WaitGroup
			wg.Add(1)

			// Wait up to 5 seconds for gpbackup to block on acquiring AccessShareLock.
			// Once blocked, we send a SIGTERM to cancel gpbackup.
			var beforeLockCount int
			go func() {
				defer wg.Done()
				waitOutputLine(outScanner, &output)

				iterations := 50
				for iterations > 0 {
					_ = backupConn.Get(&beforeLockCount, checkLockQuery)
					if beforeLockCount < 1 {
						time.Sleep(100 * time.Millisecond)
						iterations--
					} else {
						break
					}
				}
				_ = cmd.Process.Signal(unix.SIGTERM)

				readOutput(outScanner, &output)
			}()
			err := cmd.Start()
			Expect(err).ToNot(HaveOccurred())
			wg.Wait()
			Expect(beforeLockCount).To(Equal(1))

			// After gpbackup has been canceled, we should no longer see a blocked SQL
			// session trying to acquire AccessShareLock on foo2.
			var afterLockCount int
			_ = backupConn.Get(&afterLockCount, checkLockQuery)
			Expect(afterLockCount).To(Equal(0))
			backupConn.MustRollback()

			stdout := output.String()
			Expect(stdout).To(ContainSubstring("Received a termination signal, aborting backup process"))
			Expect(stdout).To(ContainSubstring("Cleanup complete"))
			Expect(stdout).To(Not(ContainSubstring("CRITICAL")))
			timestamp := getBackupTimestamp(stdout)
			assertArtifactsCleaned(timestamp)
		})
		It("runs gprestore and sends a SIGTERM to ensure cleanup functions successfully", func() {
			if useOldBackupVersion {
				Skip("This test is not needed for old backup versions")
			}
			outputBkp := gpbackup(gpbackupPath, backupHelperPath,
				"--backup-dir", backupDir,
				"--single-data-file")
			timestampBkp := getBackupTimestamp(string(outputBkp))
			args := []string{
				"--timestamp", timestampBkp,
				"--redirect-db", "restoredb",
				"--backup-dir", backupDir,
				"--verbose"}
			cmd := exec.Command(gprestorePath, args...)
			outScanner := createCombinedOutput(cmd)
			var output strings.Builder
			var wg sync.WaitGroup
			wg.Add(1)

			go func() {
				defer wg.Done()
				waitOutputLine(outScanner, &output)

				/*
				* We use a random delay for the sleep in this test (between
				* 0.5s and 1.5s) so that gprestore will be interrupted at a
				* different point in the backup process every time to help
				* catch timing issues with the cleanup.
				 */
				rng := rand.New(rand.NewSource(time.Now().UnixNano()))
				time.Sleep(time.Duration(rng.Intn(1000)+500) * time.Millisecond)
				_ = cmd.Process.Signal(unix.SIGTERM)

				readOutput(outScanner, &output)
			}()
			err := cmd.Start()
			Expect(err).ToNot(HaveOccurred())
			wg.Wait()
			stdoutRes := output.String()
			Expect(stdoutRes).To(ContainSubstring("Received a termination signal, aborting restore process"))
			Expect(stdoutRes).To(ContainSubstring("Cleanup complete"))
			Expect(stdoutRes).To(Not(ContainSubstring("CRITICAL")))
			assertArtifactsCleaned(timestampBkp)
		})
		It("runs gprestore with copy-queue-size and sends a SIGTERM to ensure cleanup functions successfully", func() {
			if useOldBackupVersion {
				Skip("This test is not needed for old backup versions")
			}
			outputBkp := gpbackup(gpbackupPath, backupHelperPath,
				"--backup-dir", backupDir,
				"--single-data-file")
			timestampBkp := getBackupTimestamp(string(outputBkp))
			args := []string{
				"--timestamp", timestampBkp,
				"--redirect-db", "restoredb",
				"--backup-dir", backupDir,
				"--verbose",
				"--copy-queue-size", "4"}
			cmd := exec.Command(gprestorePath, args...)
			outScanner := createCombinedOutput(cmd)
			var output strings.Builder
			var wg sync.WaitGroup
			wg.Add(1)

			go func() {
				defer wg.Done()
				waitOutputLine(outScanner, &output)

				/*
				* We use a random delay for the sleep in this test (between
				* 0.5s and 1.5s) so that gprestore will be interrupted at a
				* different point in the backup process every time to help
				* catch timing issues with the cleanup.
				 */
				rng := rand.New(rand.NewSource(time.Now().UnixNano()))
				time.Sleep(time.Duration(rng.Intn(1000)+500) * time.Millisecond)
				_ = cmd.Process.Signal(unix.SIGTERM)

				readOutput(outScanner, &output)
			}()
			err := cmd.Start()
			Expect(err).ToNot(HaveOccurred())
			wg.Wait()

			stdoutRes := output.String()
			Expect(stdoutRes).To(ContainSubstring("Received a termination signal, aborting restore process"))
			Expect(stdoutRes).To(ContainSubstring("Cleanup complete"))
			Expect(stdoutRes).To(Not(ContainSubstring("CRITICAL")))
			assertArtifactsCleaned(timestampBkp)
		})
	})
})
