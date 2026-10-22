package end_to_end_test

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/GreengageDB/gp-common-go-libs/testhelper"
	"github.com/GreengageDB/gpbackup/backup"
	"github.com/GreengageDB/gpbackup/testutils"
	"github.com/GreengageDB/gpbackup/utils"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

/*
 * The connections of these tests that lock tables, or wait for locks, bypass the
 * resource group of the role: with resource groups each of their transactions
 * would hold a slot of the group, which gpbackup then waits for, while the tests
 * wait for gpbackup to lock its tables.
 */
var _ = Describe("Deadlock handling", func() {
	BeforeEach(func() {
		end_to_end_setup()
		testhelper.AssertQueryRuns(backupConn, "CREATE table bigtable(id int unique); INSERT INTO bigtable SELECT generate_series(1,1000000)")
	})
	AfterEach(func() {
		end_to_end_teardown()
		testhelper.AssertQueryRuns(backupConn, "DROP table bigtable")
	})
	It("runs gpbackup with jobs flag and COPY deadlock handling occurs", func() {
		if useOldBackupVersion {
			Skip("This test is not needed for old backup versions")
		}
		// gpbackup --jobs 10 needs 12 slots of the resource group, and the transaction
		// of backupConn below one more
		if name, concurrency, ok := utils.ResourceGroupConcurrency(backupConn); ok && concurrency < 13 {
			Skip(fmt.Sprintf("This test needs 13 slots of resource group %s, which has %d", name, concurrency))
		}
		// Acquire AccessExclusiveLock on public.foo to block gpbackup when it attempts
		// to grab AccessShareLocks before its metadata dump section.
		backupConn.MustExec("BEGIN; LOCK TABLE public.foo IN ACCESS EXCLUSIVE MODE")

		// Execute gpbackup with --jobs 10 since there are 10 tables to back up
		args := []string{
			"--dbname", "testdb",
			"--backup-dir", backupDir,
			"--jobs", "10",
			"--verbose"}
		cmd := exec.Command(gpbackupPath, args...)
		// Concurrently wait for gpbackup to block when it requests an AccessShareLock on public.foo. Once
		// that happens, acquire an AccessExclusiveLock on pg_catalog.pg_trigger to block gpbackup during its
		// trigger metadata dump. Then release the initial AccessExclusiveLock on public.foo (from the
		// beginning of the test) to unblock gpbackup and let gpbackup move forward to the trigger metadata dump.
		anotherConn := testutils.SetupTestDbConnBypassingResourceGroup("testdb")
		defer anotherConn.Close()
		// The goroutines below use the connections, so they end before the connections are closed
		var lockers sync.WaitGroup
		defer lockers.Wait()
		lockers.Add(1)
		go func() {
			defer GinkgoRecover()
			defer lockers.Done()
			// gpbackup waits for its lock on public.foo until the session commits, also if the lock below fails
			committed := false
			defer func() {
				if !committed {
					_, _ = backupConn.Exec("COMMIT")
				}
			}()
			// Query to see if gpbackup's AccessShareLock request on public.foo is blocked
			checkLockQuery := `SELECT count(*) FROM pg_locks l, pg_class c, pg_namespace n WHERE l.relation = c.oid AND n.oid = c.relnamespace AND n.nspname = 'public' AND c.relname = 'foo' AND l.granted = 'f' AND l.mode = 'AccessShareLock'`

			// Wait up to 10 seconds for gpbackup to block
			var gpbackupBlockedLockCount int
			iterations := 100
			for iterations > 0 {
				_ = anotherConn.Get(&gpbackupBlockedLockCount, checkLockQuery)
				if gpbackupBlockedLockCount < 1 {
					time.Sleep(100 * time.Millisecond)
					iterations--
				} else {
					break
				}
			}

			// Queue AccessExclusiveLock request on pg_catalog.pg_trigger to block gpbackup
			// during the trigger metadata dump so that the test can queue a bunch of
			// AccessExclusiveLock requests against the test tables. Afterwards, release the
			// AccessExclusiveLock on public.foo to let gpbackup go to the trigger metadata dump.
			anotherConn.MustExec(`BEGIN; LOCK TABLE pg_catalog.pg_trigger IN ACCESS EXCLUSIVE MODE`)
			backupConn.MustExec("COMMIT")
			committed = true
		}()

		// Concurrently wait for gpbackup to block on the trigger metadata dump section. Once we
		// see gpbackup blocked, request AccessExclusiveLock (to imitate a TRUNCATE or VACUUM
		// FULL) on all the test tables.
		dataTables := []string{`public."FOObar"`, "public.foo", "public.holds", "public.sales", "public.bigtable",
			"schema2.ao1", "schema2.ao2", "schema2.foo2", "schema2.foo3", "schema2.returns"}
		for _, dataTable := range dataTables {
			lockers.Add(1)
			go func(dataTable string) {
				defer GinkgoRecover()
				defer lockers.Done()
				accessExclusiveLockConn := testutils.SetupTestDbConnBypassingResourceGroup("testdb")
				defer accessExclusiveLockConn.Close()

				// Query to see if gpbackup's AccessShareLock request on pg_catalog.pg_trigger is blocked
				checkLockQuery := `SELECT count(*) FROM pg_locks l, pg_class c, pg_namespace n WHERE l.relation = c.oid AND n.oid = c.relnamespace AND n.nspname = 'pg_catalog' AND c.relname = 'pg_trigger' AND l.granted = 'f' AND l.mode = 'AccessShareLock'`

				// Wait up to 10 seconds for gpbackup to block
				var gpbackupBlockedLockCount int
				iterations := 100
				for iterations > 0 {
					_ = accessExclusiveLockConn.Get(&gpbackupBlockedLockCount, checkLockQuery)
					if gpbackupBlockedLockCount < 1 {
						time.Sleep(100 * time.Millisecond)
						iterations--
					} else {
						break
					}
				}

				// Queue an AccessExclusiveLock request on a test table which will later
				// result in a detected deadlock during the gpbackup data dump section.
				accessExclusiveLockConn.MustExec(fmt.Sprintf(`BEGIN; LOCK TABLE %s IN ACCESS EXCLUSIVE MODE; COMMIT`, dataTable))
			}(dataTable)
		}

		// Concurrently wait for all AccessExclusiveLock requests on all 10 test tables to block.
		// Once that happens, release the AccessExclusiveLock on pg_catalog.pg_trigger to unblock
		// gpbackup and let gpbackup move forward to the data dump section.
		var accessExclBlockedLockCount int
		lockers.Add(1)
		go func() {
			defer GinkgoRecover()
			defer lockers.Done()
			// Query to check for ungranted AccessExclusiveLock requests on our test tables
			checkLockQuery := `SELECT count(*) FROM pg_locks WHERE granted = 'f' AND mode = 'AccessExclusiveLock'`

			// Wait up to 10 seconds
			iterations := 100
			for iterations > 0 {
				_ = backupConn.Get(&accessExclBlockedLockCount, checkLockQuery)
				if accessExclBlockedLockCount < 10 {
					time.Sleep(100 * time.Millisecond)
					iterations--
				} else {
					break
				}
			}

			// Unblock gpbackup by releasing AccessExclusiveLock on pg_catalog.pg_trigger
			anotherConn.MustExec("COMMIT")
		}()

		// gpbackup has finished
		output, _ := cmd.CombinedOutput()
		// The goroutines are done with the count of the locks too
		lockers.Wait()
		stdout := string(output)

		// Check that 10 deadlock traps were placed during the test
		Expect(accessExclBlockedLockCount).To(Equal(10))
		// No non-main worker should have been able to run COPY due to deadlock detection
		for i := 1; i < 10; i++ {
			expectedLockString := fmt.Sprintf("[DEBUG]:-Worker %d: LOCK TABLE ", i)
			Expect(stdout).To(ContainSubstring(expectedLockString))

			expectedWarnString := fmt.Sprintf("[WARNING]:-Worker %d could not acquire AccessShareLock for table", i)
			Expect(stdout).To(ContainSubstring(expectedWarnString))

			unexpectedCopyString := fmt.Sprintf(`[DEBUG]:-Worker %d: Executing "COPY `, i)
			Expect(stdout).ToNot(ContainSubstring(unexpectedCopyString))
		}

		// Only the main worker thread, worker 0, will run COPY on all the test tables
		for _, dataTable := range dataTables {
			expectedString := fmt.Sprintf(`[DEBUG]:-Worker 0: Executing "COPY %s`, dataTable)
			Expect(stdout).To(ContainSubstring(expectedString))
		}

		Expect(stdout).To(ContainSubstring("Backup completed successfully"))
	})
	It("runs gpbackup with copy-queue-size flag and COPY deadlock handling occurs", func() {
		if useOldBackupVersion {
			Skip("This test is not needed for old backup versions")
		}
		// Acquire AccessExclusiveLock on public.foo to block gpbackup when it attempts
		// to grab AccessShareLocks before its metadata dump section.
		backupConn.MustExec("BEGIN; LOCK TABLE public.foo IN ACCESS EXCLUSIVE MODE")

		// Execute gpbackup with --copy-queue-size 2
		args := []string{
			"--dbname", "testdb",
			"--backup-dir", backupDir,
			"--single-data-file",
			"--copy-queue-size", "2",
			"--verbose"}
		cmd := exec.Command(gpbackupPath, args...)

		// Concurrently wait for gpbackup to block when it requests an AccessShareLock on public.foo. Once
		// that happens, acquire an AccessExclusiveLock on pg_catalog.pg_trigger to block gpbackup during its
		// trigger metadata dump. Then release the initial AccessExclusiveLock on public.foo (from the
		// beginning of the test) to unblock gpbackup and let gpbackup move forward to the trigger metadata dump.
		anotherConn := testutils.SetupTestDbConnBypassingResourceGroup("testdb")
		defer anotherConn.Close()
		// The goroutines below use the connections, so they end before the connections are closed
		var lockers sync.WaitGroup
		defer lockers.Wait()
		lockers.Add(1)
		go func() {
			defer GinkgoRecover()
			defer lockers.Done()
			// gpbackup waits for its lock on public.foo until the session commits, also if the lock below fails
			committed := false
			defer func() {
				if !committed {
					_, _ = backupConn.Exec("COMMIT")
				}
			}()
			// Query to see if gpbackup's AccessShareLock request on public.foo is blocked
			checkLockQuery := `SELECT count(*) FROM pg_locks l, pg_class c, pg_namespace n WHERE l.relation = c.oid AND n.oid = c.relnamespace AND n.nspname = 'public' AND c.relname = 'foo' AND l.granted = 'f' AND l.mode = 'AccessShareLock'`

			// Wait up to 10 seconds for gpbackup to block
			var gpbackupBlockedLockCount int
			iterations := 100
			for iterations > 0 {
				_ = anotherConn.Get(&gpbackupBlockedLockCount, checkLockQuery)
				if gpbackupBlockedLockCount < 1 {
					time.Sleep(100 * time.Millisecond)
					iterations--
				} else {
					break
				}
			}

			// Queue AccessExclusiveLock request on pg_catalog.pg_trigger to block gpbackup
			// during the trigger metadata dump so that the test can queue a bunch of
			// AccessExclusiveLock requests against the test tables. Afterwards, release the
			// AccessExclusiveLock on public.foo to let gpbackup go to the trigger metadata dump.
			anotherConn.MustExec(`BEGIN; LOCK TABLE pg_catalog.pg_trigger IN ACCESS EXCLUSIVE MODE`)
			backupConn.MustExec("COMMIT")
			committed = true
		}()

		// Concurrently wait for gpbackup to block on the trigger metadata dump section. Once we
		// see gpbackup blocked, request AccessExclusiveLock (to imitate a TRUNCATE or VACUUM
		// FULL) on all the test tables.
		dataTables := []string{"public.bigtable", "public.holds", "public.foo", "schema2.foo3", "schema2.ao1",
			"schema2.ao2", `public."FOObar"`, "public.sales", "schema2.foo2", "schema2.returns"}
		for _, dataTable := range dataTables {
			lockers.Add(1)
			go func(dataTable string) {
				defer GinkgoRecover()
				defer lockers.Done()
				accessExclusiveLockConn := testutils.SetupTestDbConnBypassingResourceGroup("testdb")
				defer accessExclusiveLockConn.Close()

				// Query to see if gpbackup's AccessShareLock request on pg_catalog.pg_trigger is blocked
				checkLockQuery := `SELECT count(*) FROM pg_locks l, pg_class c, pg_namespace n WHERE l.relation = c.oid AND n.oid = c.relnamespace AND n.nspname = 'pg_catalog' AND c.relname = 'pg_trigger' AND l.granted = 'f' AND l.mode = 'AccessShareLock'`

				// Wait up to 10 seconds for gpbackup to block
				var gpbackupBlockedLockCount int
				iterations := 100
				for iterations > 0 {
					_ = accessExclusiveLockConn.Get(&gpbackupBlockedLockCount, checkLockQuery)
					if gpbackupBlockedLockCount < 1 {
						time.Sleep(100 * time.Millisecond)
						iterations--
					} else {
						break
					}
				}

				// Queue an AccessExclusiveLock request on a test table which will later
				// result in a detected deadlock during the gpbackup data dump section.
				accessExclusiveLockConn.MustExec(fmt.Sprintf(`BEGIN; LOCK TABLE %s IN ACCESS EXCLUSIVE MODE; COMMIT`, dataTable))
			}(dataTable)
		}

		// Concurrently wait for all AccessExclusiveLock requests on all 10 test tables to block.
		// Once that happens, release the AccessExclusiveLock on pg_catalog.pg_trigger to unblock
		// gpbackup and let gpbackup move forward to the data dump section.
		var accessExclBlockedLockCount int
		lockers.Add(1)
		go func() {
			defer GinkgoRecover()
			defer lockers.Done()
			// Query to check for ungranted AccessExclusiveLock requests on our test tables
			checkLockQuery := `SELECT count(*) FROM pg_locks WHERE granted = 'f' AND mode = 'AccessExclusiveLock'`

			// Wait up to 10 seconds
			iterations := 100
			for iterations > 0 {
				_ = backupConn.Get(&accessExclBlockedLockCount, checkLockQuery)
				if accessExclBlockedLockCount < 10 {
					time.Sleep(100 * time.Millisecond)
					iterations--
				} else {
					break
				}
			}

			// Unblock gpbackup by releasing AccessExclusiveLock on pg_catalog.pg_trigger
			anotherConn.MustExec("COMMIT")
		}()

		// gpbackup has finished
		output, _ := cmd.CombinedOutput()
		// The goroutines are done with the count of the locks too
		lockers.Wait()
		stdout := string(output)

		// Check that 10 deadlock traps were placed during the test
		Expect(accessExclBlockedLockCount).To(Equal(10))
		// No non-main worker should have been able to run COPY due to deadlock detection
		for i := 1; i <= 2; i++ {
			expectedLockString := fmt.Sprintf("[DEBUG]:-Worker %d: LOCK TABLE ", i)
			Expect(stdout).To(ContainSubstring(expectedLockString))

			expectedWarnString := fmt.Sprintf("[WARNING]:-Worker %d could not acquire AccessShareLock for table", i)
			Expect(stdout).To(ContainSubstring(expectedWarnString))

			unexpectedCopyString := fmt.Sprintf(`[DEBUG]:-Worker %d: Executing "COPY `, i)
			Expect(stdout).ToNot(ContainSubstring(unexpectedCopyString))

			expectedLockString = fmt.Sprintf(`Locks held on table %s`, dataTables[i-1])
			Expect(stdout).To(ContainSubstring(expectedLockString))

			Expect(stdout).To(ContainSubstring(`"Mode":"AccessExclusiveLock"`))
		}

		// Only the main worker thread, worker 0, will run COPY on all the test tables
		for _, dataTable := range dataTables {
			expectedString := fmt.Sprintf(`[DEBUG]:-Worker 0: Executing "COPY %s`, dataTable)
			Expect(stdout).To(ContainSubstring(expectedString))
		}

		Expect(stdout).To(ContainSubstring("Backup completed successfully"))
	})
	deferDeadlockedTables := func(singleDataFile bool) {
		if useOldBackupVersion || backupConn.Version.Before(backup.SNAPSHOT_GPDB_MIN_VERSION) {
			Skip(fmt.Sprintf("This test is not needed for old backup versions or GPDB versions < %s", backup.SNAPSHOT_GPDB_MIN_VERSION))
		}
		// Acquire AccessExclusiveLock on public.foo to block gpbackup when it attempts
		// to grab AccessShareLocks before its metadata dump section.
		backupConn.MustExec("BEGIN; LOCK TABLE public.foo IN ACCESS EXCLUSIVE MODE")

		args := []string{
			"--dbname", "testdb",
			"--backup-dir", backupDir,
			"--jobs", "2",
			"--verbose"}
		if singleDataFile {
			args = append(args, "--single-data-file")
		}
		cmd := exec.Command(gpbackupPath, args...)
		// Concurrently wait for gpbackup to block when it requests an AccessShareLock on public.foo. Once
		// that happens, acquire an AccessExclusiveLock on pg_catalog.pg_trigger to block gpbackup during its
		// trigger metadata dump. Then release the initial AccessExclusiveLock on public.foo (from the
		// beginning of the test) to unblock gpbackup and let gpbackup move forward to the trigger metadata dump.
		anotherConn := testutils.SetupTestDbConnBypassingResourceGroup("testdb")
		defer anotherConn.Close()
		// The goroutines below use the connections, so they end before the connections are closed
		var lockers sync.WaitGroup
		defer lockers.Wait()
		lockers.Add(1)
		go func() {
			defer GinkgoRecover()
			defer lockers.Done()
			// gpbackup waits for its lock on public.foo until the session commits, also if the lock below fails
			committed := false
			defer func() {
				if !committed {
					_, _ = backupConn.Exec("COMMIT")
				}
			}()
			// Query to see if gpbackup's AccessShareLock request on public.foo is blocked
			checkLockQuery := `SELECT count(*) FROM pg_locks l, pg_class c, pg_namespace n WHERE l.relation = c.oid AND n.oid = c.relnamespace AND n.nspname = 'public' AND c.relname = 'foo' AND l.granted = 'f' AND l.mode = 'AccessShareLock'`

			// Wait up to 10 seconds for gpbackup to block
			var gpbackupBlockedLockCount int
			iterations := 100
			for iterations > 0 {
				_ = anotherConn.Get(&gpbackupBlockedLockCount, checkLockQuery)
				if gpbackupBlockedLockCount < 1 {
					time.Sleep(100 * time.Millisecond)
					iterations--
				} else {
					break
				}
			}

			// Queue AccessExclusiveLock request on pg_catalog.pg_trigger to block gpbackup
			// during the trigger metadata dump so that the test can queue a bunch of
			// AccessExclusiveLock requests against the test tables. Afterwards, release the
			// AccessExclusiveLock on public.foo to let gpbackup go to the trigger metadata dump.
			anotherConn.MustExec(`BEGIN; LOCK TABLE pg_catalog.pg_trigger IN ACCESS EXCLUSIVE MODE`)
			backupConn.MustExec("COMMIT")
			committed = true
		}()

		// Concurrently wait for gpbackup to block on the trigger metadata dump section. Once we
		// see gpbackup blocked, request AccessExclusiveLock (to imitate a TRUNCATE or VACUUM
		// FULL) on two of the test tables.
		dataTables := []string{"public.holds", "public.sales", "public.bigtable",
			"schema2.ao1", "schema2.ao2", "schema2.foo2", "schema2.foo3", "schema2.returns"}
		lockedTables := []string{`public."FOObar"`, "public.foo"}
		for _, lockedTable := range lockedTables {
			lockers.Add(1)
			go func(lockedTable string) {
				defer GinkgoRecover()
				defer lockers.Done()
				accessExclusiveLockConn := testutils.SetupTestDbConnBypassingResourceGroup("testdb")
				defer accessExclusiveLockConn.Close()

				// Query to see if gpbackup's AccessShareLock request on pg_catalog.pg_trigger is blocked
				checkLockQuery := `SELECT count(*) FROM pg_locks l, pg_class c, pg_namespace n WHERE l.relation = c.oid AND n.oid = c.relnamespace AND n.nspname = 'pg_catalog' AND c.relname = 'pg_trigger' AND l.granted = 'f' AND l.mode = 'AccessShareLock'`

				// Wait up to 10 seconds for gpbackup to block
				var gpbackupBlockedLockCount int
				iterations := 100
				for iterations > 0 {
					_ = accessExclusiveLockConn.Get(&gpbackupBlockedLockCount, checkLockQuery)
					if gpbackupBlockedLockCount < 1 {
						time.Sleep(100 * time.Millisecond)
						iterations--
					} else {
						break
					}
				}
				// Queue an AccessExclusiveLock request on a test table which will later
				// result in a detected deadlock during the gpbackup data dump section.
				accessExclusiveLockConn.MustExec(fmt.Sprintf(`BEGIN; LOCK TABLE %s IN ACCESS EXCLUSIVE MODE; COMMIT`, lockedTable))
			}(lockedTable)
		}

		// Concurrently wait for all AccessExclusiveLock requests on all 10 test tables to block.
		// Once that happens, release the AccessExclusiveLock on pg_catalog.pg_trigger to unblock
		// gpbackup and let gpbackup move forward to the data dump section.
		var accessExclBlockedLockCount int
		lockers.Add(1)
		go func() {
			defer GinkgoRecover()
			defer lockers.Done()
			// Query to check for ungranted AccessExclusiveLock requests on our test tables
			checkLockQuery := `SELECT count(*) FROM pg_locks WHERE granted = 'f' AND mode = 'AccessExclusiveLock'`

			// Wait up to 10 seconds
			iterations := 100
			for iterations > 0 {
				_ = backupConn.Get(&accessExclBlockedLockCount, checkLockQuery)
				if accessExclBlockedLockCount < 9 {
					time.Sleep(100 * time.Millisecond)
					iterations--
				} else {
					break
				}
			}

			// Unblock gpbackup by releasing AccessExclusiveLock on pg_catalog.pg_trigger
			anotherConn.MustExec("COMMIT")
		}()

		// gpbackup has finished
		output, _ := cmd.CombinedOutput()
		// The goroutines are done with the count of the locks too
		lockers.Wait()
		stdout := string(output)

		// Check that 2 deadlock traps were placed during the test
		Expect(accessExclBlockedLockCount).To(Equal(2))
		// No non-main worker should have been able to run COPY due to deadlock detection
		for i := 1; i < backupConn.NumConns; i++ {
			expectedLockString := fmt.Sprintf("[DEBUG]:-Worker %d: LOCK TABLE ", i)
			Expect(stdout).To(ContainSubstring(expectedLockString))

			expectedWarnString := fmt.Sprintf("[WARNING]:-Worker %d could not acquire AccessShareLock for table", i)
			Expect(stdout).To(ContainSubstring(expectedWarnString))

			unexpectedCopyString := fmt.Sprintf(`[DEBUG]:-Worker %d: Executing "COPY `, i)
			Expect(stdout).To(ContainSubstring(unexpectedCopyString))
		}

		// Only the main worker thread, worker 0, will run COPY on the 2 locked test tables
		for _, lockedTable := range lockedTables {
			expectedString := fmt.Sprintf(`[DEBUG]:-Worker 0: Executing "COPY %s`, lockedTable)
			Expect(stdout).To(ContainSubstring(expectedString))
		}
		for _, dataTable := range dataTables {
			unexpectedString := fmt.Sprintf(`[DEBUG]:-Worker 0: Executing "COPY %s`, dataTable)
			Expect(stdout).ToNot(ContainSubstring(unexpectedString))
		}
		Expect(stdout).To(ContainSubstring("Backup completed successfully"))

		if singleDataFile {
			// The main worker writes the deferred tables to the slots it takes in the data files
			timestamp := getBackupTimestamp(stdout)
			gprestore(gprestorePath, restoreHelperPath, timestamp,
				"--redirect-db", "restoredb",
				"--jobs", "2",
				"--backup-dir", backupDir)
			assertDataRestored(restoreConn, publicSchemaTupleCounts)
			assertDataRestored(restoreConn, schema2TupleCounts)
			assertDataRestored(restoreConn, map[string]int{"public.bigtable": 1000000})
			assertArtifactsCleaned(timestamp)
		}
	}
	It("runs gpbackup and defers 2 deadlocked tables to main worker", func() {
		deferDeadlockedTables(false)
	})
	It("runs gpbackup with single-data-file and defers 3 tables in a row to main worker while it copies a large table", func() {
		if useOldBackupVersion || backupConn.Version.Before(backup.SNAPSHOT_GPDB_MIN_VERSION) {
			Skip(fmt.Sprintf("This test is not needed for old backup versions or GPDB versions < %s", backup.SNAPSHOT_GPDB_MIN_VERSION))
		}
		// The first table of the backup is large, so that the main worker is still copying
		// it once worker 1 has deferred the next two tables as well
		testhelper.AssertQueryRuns(backupConn, `CREATE SCHEMA pipe_test;
			CREATE TABLE pipe_test.t1 (a int) DISTRIBUTED BY (a);
			INSERT INTO pipe_test.t1 SELECT generate_series(1, 3000000)`)
		defer testhelper.AssertQueryRuns(backupConn, "DROP SCHEMA pipe_test CASCADE")
		defer testhelper.AssertQueryRuns(restoreConn, "DROP SCHEMA IF EXISTS pipe_test CASCADE")
		for i := 2; i <= 6; i++ {
			testhelper.AssertQueryRuns(backupConn, fmt.Sprintf(`CREATE TABLE pipe_test.t%[1]d (a int) DISTRIBUTED BY (a);
				INSERT INTO pipe_test.t%[1]d SELECT generate_series(1, 1000)`, i))
		}
		testhelper.AssertQueryRuns(backupConn, "ANALYZE pipe_test.t1")

		anotherConn := testutils.SetupTestDbConnBypassingResourceGroup("testdb")
		defer anotherConn.Close()
		waitForCount := func(query string, count int) {
			var actual int
			for iterations := 0; iterations < 300; iterations++ {
				_ = anotherConn.Get(&actual, query)
				if actual >= count {
					return
				}
				time.Sleep(100 * time.Millisecond)
			}
			Fail(fmt.Sprintf("Timed out waiting for %d: %s", count, query))
		}
		ungrantedLocks := func(relname string, mode string) string {
			return fmt.Sprintf(`SELECT count(*) FROM pg_locks l JOIN pg_class c ON c.oid = l.relation
				WHERE c.relname = '%s' AND l.mode = '%s' AND NOT l.granted`, relname, mode)
		}

		// Block gpbackup while it locks the tables, then in its metadata dump
		backupConn.MustExec("BEGIN; LOCK TABLE pipe_test.t1 IN ACCESS EXCLUSIVE MODE")
		// On a failure, the shared connection does not keep its transaction for the next tests,
		// nor the DROP of the schema above in it
		committed := false
		defer func() {
			if !committed {
				_, _ = backupConn.Exec("COMMIT")
			}
		}()
		cmd := exec.Command(gpbackupPath, "--dbname", "testdb", "--backup-dir", backupDir,
			"--single-data-file", "--include-schema", "pipe_test", "--verbose")
		var combinedOutput strings.Builder
		cmd.Stdout, cmd.Stderr = &combinedOutput, &combinedOutput
		// Buffered, so that the goroutine ends also if the test fails before it reads the output
		output := make(chan string, 1)
		Expect(cmd.Start()).To(Succeed())
		go func() {
			defer GinkgoRecover()
			_ = cmd.Wait()
			output <- combinedOutput.String()
		}()
		// A gpbackup that a failure leaves waiting for a lock is interrupted, so that its cleanup runs
		outputRead := false
		defer func() {
			if !outputRead {
				_ = cmd.Process.Signal(os.Interrupt)
				select {
				case <-output:
				case <-time.After(2 * time.Minute):
				}
			}
		}()
		waitForCount(ungrantedLocks("t1", "AccessShareLock"), 1)
		anotherConn.MustExec("BEGIN; LOCK TABLE pg_catalog.pg_trigger IN ACCESS EXCLUSIVE MODE")
		backupConn.MustExec("COMMIT")
		committed = true
		waitForCount(ungrantedLocks("pg_trigger", "AccessShareLock"), 1)

		// Queue ACCESS EXCLUSIVE lock requests on the first 3 tables, behind the locks of
		// gpbackup, so that worker 1 cannot lock them and defers them to the main worker
		for i := 1; i <= 3; i++ {
			go func(table int) {
				defer GinkgoRecover()
				lockConn := testutils.SetupTestDbConnBypassingResourceGroup("testdb")
				defer lockConn.Close()
				lockConn.MustExec(fmt.Sprintf("BEGIN; LOCK TABLE pipe_test.t%d IN ACCESS EXCLUSIVE MODE; COMMIT", table))
			}(i)
		}
		waitForCount(`SELECT count(*) FROM pg_locks WHERE locktype = 'relation' AND mode = 'AccessExclusiveLock' AND NOT granted`, 3)
		anotherConn.MustExec("COMMIT")

		stdout := <-output
		outputRead = true
		for i := 1; i <= 3; i++ {
			Expect(stdout).To(ContainSubstring(fmt.Sprintf("[WARNING]:-Worker 1 could not acquire AccessShareLock for table pipe_test.t%d.", i)))
		}
		Expect(stdout).To(ContainSubstring("Backup completed successfully"))

		timestamp := getBackupTimestamp(stdout)
		gprestore(gprestorePath, restoreHelperPath, timestamp,
			"--redirect-db", "restoredb",
			"--backup-dir", backupDir)
		assertDataRestored(restoreConn, map[string]int{"pipe_test.t1": 3000000, "pipe_test.t2": 1000, "pipe_test.t3": 1000,
			"pipe_test.t4": 1000, "pipe_test.t5": 1000, "pipe_test.t6": 1000})
		assertArtifactsCleaned(timestamp)
	})
	It("runs gpbackup and defers 2 deadlocked tables to main worker with single-data-file and jobs", func() {
		deferDeadlockedTables(true)
	})
})
