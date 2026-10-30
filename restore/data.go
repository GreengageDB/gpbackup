package restore

/*
 * This file contains structs and functions related to backing up data on the segments.
 */

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/GreengageDB/gp-common-go-libs/cluster"
	"github.com/GreengageDB/gp-common-go-libs/dbconn"
	"github.com/GreengageDB/gp-common-go-libs/gplog"
	"github.com/GreengageDB/gpbackup/filepath"
	"github.com/GreengageDB/gpbackup/options"
	"github.com/GreengageDB/gpbackup/toc"
	"github.com/GreengageDB/gpbackup/utils"
	"github.com/jackc/pgconn"
	"github.com/pkg/errors"
	"gopkg.in/cheggaaa/pb.v1"
)

var (
	tableDelim = ","
	maxHelpers int
)

/*
 * errorFile is the error file of the gpbackup_helper that creates the pipe
 * destinationToRead, with --single-data-file or --resize-cluster.
 */
func CopyTableIn(queryContext context.Context, connectionPool *dbconn.DBConn, tableName string, tableAttributes string, destinationToRead string, singleDataFile bool, whichConn int, errorFile string) (int64, error) {
	if wasTerminated.Load() {
		return -1, nil
	}
	whichConn = connectionPool.ValidateConnNum(whichConn)
	copyCommand := ""
	readFromDestinationCommand := "cat"
	customPipeThroughCommand := utils.GetPipeThroughProgram().InputCommand
	resizeCluster := MustGetFlagBool(options.RESIZE_CLUSTER)

	if singleDataFile || resizeCluster {
		//helper.go handles compression, so we don't want to set it here
		customPipeThroughCommand = utils.DefaultPipeThroughProgram
		readFromDestinationCommand = fmt.Sprintf("(timeout --foreground 300 bash -c \"while [[ ! -p \"%s\" && ! -f \"%s\" ]]; do sleep 1; done\" || (echo \"Pipe not found %s\">&2; exit 1)) && (test ! -f \"%s\" || (echo \"Error file found %s\">&2; exit 1)) && %s", destinationToRead, errorFile, destinationToRead, errorFile, errorFile, readFromDestinationCommand)
	} else if MustGetFlagString(options.PLUGIN_CONFIG) != "" {
		readFromDestinationCommand = fmt.Sprintf("%s restore_data %s", pluginConfig.ExecutablePath, pluginConfig.ConfigPath)
	}

	if customPipeThroughCommand == utils.DefaultPipeThroughProgram {
		copyCommand = fmt.Sprintf("PROGRAM '%s %s'", readFromDestinationCommand, destinationToRead)
	} else {
		copyCommand = fmt.Sprintf("PROGRAM '%s %s | %s'", readFromDestinationCommand, destinationToRead, customPipeThroughCommand)
	}

	query := fmt.Sprintf("COPY %s%s FROM %s WITH CSV DELIMITER '%s' ON SEGMENT;", tableName, tableAttributes, copyCommand, tableDelim)

	if connectionPool.Version.AtLeast("7") {
		utils.LogProgress(`Executing "%s" on coordinator`, query)
	} else {
		utils.LogProgress(`Executing "%s" on master`, query)
	}
	result, err := connectionPool.ExecContext(queryContext, query, whichConn)
	if err != nil {
		errStr := fmt.Sprintf("Error loading data into table %s", tableName)

		// The COPY ON SEGMENT error might contain useful CONTEXT output
		if pgErr, ok := err.(*pgconn.PgError); ok && pgErr.Where != "" {
			errStr = fmt.Sprintf("%s: %s", errStr, pgErr.Where)
		}

		err = errors.Wrap(err, errStr)

		return 0, err
	}

	rowsLoaded, _ := result.RowsAffected()

	return rowsLoaded, nil
}

// copyDone is called once the COPY of every batch of the table has succeeded, before its rows are checked and
// redistributed
func restoreSingleTableData(queryContext context.Context, fpInfo *filepath.FilePathInfo, entry toc.CoordinatorDataEntry, tableName string, whichConn int, helperIdx []int, copyDone func()) error {
	origSize, destSize, resizeCluster, batches := GetResizeClusterInfo()

	var numRowsRestored int64
	// We don't want duplicate data for replicated tables so only do one batch
	if entry.IsReplicated {
		batches = 1
	}
	for i := 0; i < batches; i++ {
		destinationToRead := ""
		if backupConfig.SingleDataFile || resizeCluster {
			destinationToRead = fmt.Sprintf("%s_%d_%d", fpInfo.GetSegmentPipePathForCopyCommand(helperIdx...), entry.Oid, i)
		} else {
			destinationToRead = fpInfo.GetTableBackupFilePathForCopyCommand(entry.Oid, utils.GetPipeThroughProgram().Extension, backupConfig.SingleDataFile)
		}
		gplog.Debug("Reading from %s", destinationToRead)

		if entry.DistByEnum {
			gplog.Verbose("Setting gp_enable_segment_copy_checking TO off for table %s", tableName)
			connectionPool.MustExec("SET gp_enable_segment_copy_checking TO off;", whichConn)
			// In a transaction that the failure of the COPY has aborted, the RESET fails,
			// and the rollback of the transaction resets the setting instead
			defer func() {
				_, _ = connectionPool.Exec("RESET gp_enable_segment_copy_checking;", whichConn)
			}()
		}

		/*
		 * An error file means that the restore_helper has encountered an error and
		 * has shut down, so that the COPY would wait for pipes the helper will not
		 * create.  The COPY fails on a segment that finds the error file of its
		 * helper, and a failed COPY is checked for it on all segments.
		 */
		errorFile := utils.GetErrorFilename(fpInfo.GetSegmentPipePathForCopyCommand(helperIdx...))
		partialRowsRestored, copyErr := CopyTableIn(queryContext, connectionPool, tableName, entry.AttributeString, destinationToRead, backupConfig.SingleDataFile, whichConn, errorFile)

		if copyErr != nil {
			gplog.Error(copyErr.Error())
			if backupConfig.SingleDataFile || resizeCluster {
				agentErr := checkAgentErrors(*fpInfo, helperIdx)
				if agentErr != nil {
					gplog.Error(agentErr.Error())
					return agentErr
				}
			}
			if MustGetFlagBool(options.ON_ERROR_CONTINUE) {
				if connectionPool.Version.AtLeast("6") && (backupConfig.SingleDataFile || resizeCluster) {
					// inform segment helpers to skip this entry
					utils.CreateSkipFileOnSegments(entry.Oid, tableName, globalCluster, *fpInfo, helperIdx...)
				}
			}
			return copyErr
		}
		numRowsRestored += partialRowsRestored
	}

	numRowsBackedUp := entry.RowsCopied

	// For replicated tables, we don't restore second and subsequent batches of data in the larger-to-smaller case,
	// as that would duplicate data, so we have to "scale down" the values to determine whether the correct number
	// of rows was restored
	if entry.IsReplicated && origSize > destSize {
		numRowsBackedUp /= int64(origSize)
		numRowsRestored /= int64(destSize)
	}

	err := CheckRowsRestored(numRowsRestored, numRowsBackedUp, tableName)
	if err != nil {
		gplog.Error(err.Error())
		/*
		 * A helper that stops during the COPY closes its pipe after it creates its
		 * error file, so that the COPY may end without an error and with fewer rows.
		 * The helper is then found to have stopped, as for a failed COPY, before the
		 * COPY is recorded as ended.
		 */
		if backupConfig.SingleDataFile || resizeCluster {
			if agentErr := checkAgentErrors(*fpInfo, helperIdx); agentErr != nil {
				gplog.Error(agentErr.Error())
				return agentErr
			}
		}
		return err
	}
	copyDone()

	if resizeCluster || entry.DistByEnum {
		// replicated tables cannot be redistributed, so instead expand them if needed
		if entry.IsReplicated && (origSize < destSize) {
			err = ExpandReplicatedTable(origSize, tableName, whichConn)
			if err != nil {
				err = errors.Wrapf(err, "Error expanding replicated table %s", tableName)
			}
		} else {
			err = RedistributeTableData(tableName, whichConn)
			if err != nil {
				err = errors.Wrapf(err, "Error redistributing table %s", tableName)
			}
		}
		if err != nil {
			gplog.Error(err.Error())
			return err
		}
	}

	return nil
}

func ExpandReplicatedTable(origSize int, tableName string, whichConn int) error {
	// Replicated tables will only be initially restored to the segments backup was run from, and
	// redistributing does not cause the data to be replicated to the new segments.
	// To work around this, update the distribution policy entry for those tables to the original cluster size
	// and then explicitly expand them to cause the data to be replicated to all new segments.
	gplog.Debug("Distributing replicated data for %s", tableName)
	alterDistPolQuery := fmt.Sprintf("UPDATE gp_distribution_policy SET numsegments=%d WHERE localoid = '%s'::regclass::oid", origSize, utils.EscapeSingleQuotes(tableName))
	_, err := connectionPool.Exec(alterDistPolQuery, whichConn)
	if err != nil {
		return err
	}

	expandTableQuery := fmt.Sprintf("ALTER TABLE %s EXPAND TABLE;", tableName)
	_, err = connectionPool.Exec(expandTableQuery, whichConn)
	if err != nil {
		return err
	}

	return nil
}

func CheckRowsRestored(rowsRestored int64, rowsBackedUp int64, tableName string) error {
	if rowsRestored != rowsBackedUp {
		rowsErrMsg := fmt.Sprintf("Expected to restore %d rows to table %s, but restored %d instead", rowsBackedUp, tableName, rowsRestored)
		return errors.New(rowsErrMsg)
	}
	return nil
}

func RedistributeTableData(tableName string, whichConn int) error {
	gplog.Debug("Redistributing data for %s", tableName)
	query := fmt.Sprintf("ALTER TABLE %s SET WITH (REORGANIZE=true)", tableName)
	_, err := connectionPool.Exec(query, whichConn)
	return err
}

func restoreDataFromTimestamp(fpInfo filepath.FilePathInfo, dataEntries []toc.CoordinatorDataEntry,
	gucStatements []toc.StatementWithType, dataProgressBar utils.ProgressBar) int32 {
	totalTables := len(dataEntries)
	if totalTables == 0 {
		gplog.Verbose("No data to restore for timestamp = %s", fpInfo.Timestamp)
		return 0
	}

	_, _, resizeCluster, batches := GetResizeClusterInfo()
	/*
	 * With --single-data-file, the gpbackup_helper of each data file reads its tables
	 * one after another, and a group of connsPerDataFile connections restores them:
	 * all connections with a single data file, and with several data files as many
	 * as --copy-queue-size gives, or more with fewer data files to restore than
	 * groups.  Without --single-data-file, all tables are in data file 0.
	 */
	numDataFiles := 1
	if backupConfig.SingleDataFile {
		numDataFiles = toc.GetNumDataFiles(dataEntries)
	}
	connsPerDataFile := numConnsPerDataFile(dataEntries, connectionPool.NumConns, MustGetFlagInt(options.COPY_QUEUE_SIZE))
	groups := NewDataFileGroups(dataEntries, numDataFiles, connectionPool.NumConns, connsPerDataFile)
	if backupConfig.SingleDataFile || resizeCluster {
		msg := ""
		if backupConfig.SingleDataFile {
			msg += "single data file "
		}
		if resizeCluster {
			msg += "resize "
		}
		gplog.Verbose("Initializing pipes and gpbackup_helper on segments for %srestore", msg)
		utils.VerifyHelperVersionOnSegments(version, globalCluster)

		if backupConfig.SingleDataFile {
			maxHelpers = numDataFiles
		} else if connectionPool.NumConns < totalTables {
			maxHelpers = connectionPool.NumConns
		} else {
			maxHelpers = totalTables
		}

		if backupConfig.SingleDataFile {
			/*
			 * The helper of a data file is started once a group of connections takes
			 * the data file, so that it does not hold the data file open, which a
			 * plugin may be streaming, while the group restores other data files.
			 */
			groups.StartDataFile = func(file int) {
				startRestoreHelper(fpInfo, dataEntries, groups.entriesOfFile[file], file, connsPerDataFile*batches)
			}
		} else {
			// During a larger-to-smaller restore, we need to do multiple passes of
			// data loading so we assign the batches here.
			for whichConn := 0; whichConn < maxHelpers; whichConn++ {
				entryIdxs := make([]int, 0)
				for entryIdx := whichConn; entryIdx < totalTables; entryIdx += maxHelpers {
					entryIdxs = append(entryIdxs, entryIdx)
				}
				startRestoreHelper(fpInfo, dataEntries, entryIdxs, whichConn, connectionPool.NumConns*batches)
			}
		}
	}
	/*
	 * With --on-error-continue, the gpbackup_helpers that have stopped: that of each
	 * data file, or with --resize-cluster that of each connection.  Their remaining
	 * tables are reported as errors without being truncated or checked for errors on
	 * the segments again.
	 */
	stoppedHelpers := make([]atomic.Bool, maxHelpers+1)
	/*
	 * With --single-data-file, each table is restored in a transaction, which begins
	 * in the order of the data file, as described at DataFileGroups.  On GPDB 7, the
	 * COPY into a partition locks all its ancestors until the end of its transaction,
	 * so the transaction locks them in its turn as well.
	 *
	 * A table is truncated on its own before its data is restored, and with
	 * --single-data-file in its turn, before its transaction begins: the TRUNCATE
	 * takes a slot of its resource group as well, which it would otherwise hold
	 * while it waits for the lock of its table, ahead of the transactions of the
	 * tables before it.  A table whose TRUNCATE fails is not restored, as a table
	 * whose COPY fails.
	 */
	truncateTable := MustGetFlagBool(options.INCREMENTAL) || MustGetFlagBool(options.TRUNCATE_TABLE)
	groups.OrderTransactions = backupConfig.SingleDataFile
	groups.LimitAhead = backupConfig.SingleDataFile
	partitionAncestors := make(map[string][]string)
	if backupConfig.SingleDataFile && connectionPool.Version.AtLeast("7") {
		var err error
		partitionAncestors, err = GetPartitionAncestorFQNs()
		gplog.FatalOnError(err)
	}
	/*
	 * We break when an interrupt is received and rely on
	 * TerminateHangingCopySessions to stop any COPY
	 * statements in progress if they don't finish on their own.
	 */
	var tableNum int64 = 0
	var workerPool sync.WaitGroup
	var numErrors int32
	var mutex = &sync.Mutex{}
	panicChan := make(chan error)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // Make sure it's called to release resources even if no errors
	groups.WakeWhenDone(ctx)
	onErrorContinue := MustGetFlagBool(options.ON_ERROR_CONTINUE)

	for i := 0; i < connectionPool.NumConns; i++ {
		workerPool.Add(1)
		go func(whichConn int) {
			defer func() {
				if panicErr := recover(); panicErr != nil {
					cancel()
					panicChan <- fmt.Errorf("%v", panicErr)
				}
			}()
			defer workerPool.Done()

			// A table left by a stopped helper is reported as an error without being truncated
			skipTableOfStoppedHelper := func(entryIdx int, tableName string) {
				groups.TurnDone(entryIdx)
				gplog.Verbose("Skipping data of table %s, as the gpbackup_helper of its data has stopped", tableName)
				atomic.AddInt32(&numErrors, 1)
				mutex.Lock()
				errorTablesData[tableName] = Empty{}
				mutex.Unlock()
				groups.CopyDone(entryIdx)
				dataProgressBar.Increment()
			}

			setGUCsForConnection(gucStatements, whichConn)
			nextEntryIdx := groups.EntriesOfConn(ctx, whichConn)
			if resizeCluster && !backupConfig.SingleDataFile {
				// The gpbackup_helper of the connection has the pipes of these entries only
				nextEntryIdx = entriesOfHelper(whichConn, maxHelpers, totalTables)
			}
			for entryIdx, ok := nextEntryIdx(); ok; entryIdx, ok = nextEntryIdx() {
				entry := dataEntries[entryIdx]
				helperIdx := HelperIdx(whichConn)
				if backupConfig.SingleDataFile {
					helperIdx = HelperIdx(entry.DataFileIdx)
				}
				// Check if any error occurred in any other goroutines:
				select {
				case <-ctx.Done():
					return // Error somewhere, terminate
				default: // Default is must to avoid blocking
				}
				if wasTerminated.Load() {
					dataProgressBar.(*pb.ProgressBar).NotPrint = true
					cancel()
					return
				}
				tableName := utils.MakeFQN(entry.Schema, entry.Name)
				if opts.RedirectSchema != "" {
					tableName = utils.MakeFQN(opts.RedirectSchema, entry.Name)
				}
				helper := 0
				if len(helperIdx) > 0 {
					helper = helperIdx[0]
				}
				if (backupConfig.SingleDataFile || resizeCluster) && stoppedHelpers[helper].Load() {
					skipTableOfStoppedHelper(entryIdx, tableName)
					continue
				}
				var err error
				inTransaction := backupConfig.SingleDataFile
				if !groups.WaitForTurn(ctx, entryIdx) {
					return // Error somewhere, terminate
				}
				if wasTerminated.Load() {
					// As at the start of the loop, as the restore may have been terminated while the table waited for its turn
					dataProgressBar.(*pb.ProgressBar).NotPrint = true
					cancel()
					return
				}
				if truncateTable {
					gplog.Verbose("Truncating table %s prior to restoring data", tableName)
					_, err = connectionPool.Exec(`TRUNCATE `+tableName, whichConn)
				}
				if err == nil && inTransaction {
					err = beginTableTransaction(whichConn)
				}
				if ancestors := partitionAncestors[tableName]; err == nil && len(ancestors) > 0 {
					err = lockAncestors(ancestors, whichConn)
				}
				groups.TurnDone(entryIdx)
				if err != nil {
					gplog.Error(err.Error())
					// The COPY of the table is not run, so its helpers are told to skip it
					if onErrorContinue && connectionPool.Version.AtLeast("6") && (backupConfig.SingleDataFile || resizeCluster) {
						utils.CreateSkipFileOnSegments(entry.Oid, tableName, globalCluster, fpInfo, helperIdx...)
					}
				}
				if err == nil {
					err = restoreSingleTableData(ctx, &fpInfo, entry, tableName, whichConn, helperIdx, func() { groups.CopyDone(entryIdx) })
				}
				if inTransaction && connectionPool.Tx[whichConn] != nil {
					if err == nil {
						err = connectionPool.Commit(whichConn)
						if err != nil {
							gplog.Error(err.Error())
						}
					} else {
						// No rows of the table are left behind
						_ = connectionPool.Rollback(whichConn)
					}
				}

				if err != nil {
					atomic.AddInt32(&numErrors, 1)
					if errors.Is(err, utils.AgentErr) && onErrorContinue {
						// The helper has stopped, but the tables of the other helpers can still be restored
						dataProgressBar.(*pb.ProgressBar).NotPrint = true
						if stoppedHelpers[helper].CompareAndSwap(false, true) {
							gplog.Warn("The gpbackup_helper %d has stopped on a segment, so its tables left are not restored, "+
								"and its helpers on the other segments are told to skip them", helper)
							utils.CreateSkipFilesOnSegments(oidsOfHelper(groups, dataEntries, helper, maxHelpers), globalCluster, fpInfo, helperIdx...)
						}
					} else if !onErrorContinue {
						dataProgressBar.(*pb.ProgressBar).NotPrint = true
						cancel()
						return
					}
					mutex.Lock()
					errorTablesData[tableName] = Empty{}
					mutex.Unlock()
				} else {
					utils.LogProgress("Restored data to table %s from file (table %d of %d)", tableName, atomic.AddInt64(&tableNum, 1), totalTables)
				}

				// Also for a table whose COPY has failed or has not run, after its skip file is created
				groups.CopyDone(entryIdx)
				dataProgressBar.Increment()
			}
		}(i)
	}
	workerPool.Wait()
	// Allow panics to crash from the main process, invoking DoCleanup
	select {
	case err := <-panicChan:
		gplog.Fatal(err, "")
	default:
		// no panic, nothing to do
	}

	if numErrors > 0 {
		fmt.Println("")
		gplog.Error("Encountered %d error(s) during table data restore; see log file %s for a list of table errors.", numErrors, gplog.GetLogFilePath())
	}

	return numErrors
}

/*
 * startRestoreHelper starts the gpbackup_helper with the given index, which restores
 * the given data entries, with numPipes of their pipes created ahead.  Once
 * gprestore is being terminated, it waits for the cleanup to exit the program.
 */
func startRestoreHelper(fpInfo filepath.FilePathInfo, dataEntries []toc.CoordinatorDataEntry, entryIdxs []int, whichHelper int, numPipes int) {
	origSize, destSize, resizeCluster, batches := GetResizeClusterInfo()
	oidList := make([]string, 0)
	for _, entryIdx := range entryIdxs {
		if dataEntries[entryIdx].IsReplicated {
			oidList = append(oidList, fmt.Sprintf("%d,0", dataEntries[entryIdx].Oid))
			continue
		}

		for b := 0; b < batches; b++ {
			oidList = append(oidList, fmt.Sprintf("%d,%d", dataEntries[entryIdx].Oid, b))
		}
	}

	// The helper of a data file may be started while gprestore is being terminated
	unlock := utils.LockHelperStart(&wasTerminated)
	defer unlock()
	utils.WriteOidListToSegments(oidList, globalCluster, fpInfo, HelperIdx(whichHelper)...)
	initialPipes := CreateInitialSegmentPipes(oidList, globalCluster, numPipes, fpInfo, HelperIdx(whichHelper)...)
	isFilter := false
	if len(opts.IncludedRelations) > 0 || len(opts.ExcludedRelations) > 0 || len(opts.IncludedSchemas) > 0 || len(opts.ExcludedSchemas) > 0 {
		isFilter = true
	}
	compressStr := ""
	if backupConfig.Compressed {
		compressStr = fmt.Sprintf(" --compression-type %s ", utils.GetPipeThroughProgram().Name)
	}
	// The helper of a data file reads its own data file and segment TOC
	dataFileIdx := 0
	if backupConfig.SingleDataFile {
		dataFileIdx = whichHelper
	}
	utils.StartGpbackupHelpers(globalCluster, fpInfo, "--restore-agent", MustGetFlagString(options.PLUGIN_CONFIG), compressStr, MustGetFlagBool(options.ON_ERROR_CONTINUE), isFilter, initialPipes, backupConfig.SingleDataFile, resizeCluster, origSize, destSize, dataFileIdx, gplog.GetVerbosity(), HelperIdx(whichHelper)...)
}

func CreateInitialSegmentPipes(oidList []string, c *cluster.Cluster, numConns int, fpInfo filepath.FilePathInfo, helperIdx ...int) int {
	// Create min(connections, tables) segment pipes on each host
	var maxPipes int
	if !backupConfig.SingleDataFile {
		maxPipes = 1 // Create single initial pipe for the first oid in the restore oids list for non --single-data-file data file restore.
	} else if numConns < len(oidList) {
		maxPipes = numConns
	} else {
		maxPipes = len(oidList)
	}
	utils.CreateSegmentPipesOnAllHostsForRestore(oidList[:maxPipes], c, fpInfo, helperIdx...)
	return maxPipes
}

/*
 * oidsOfHelper returns the oids of the data entries of a gpbackup_helper: those of
 * its data file with --single-data-file, or else those of its connection.
 */
func oidsOfHelper(groups *DataFileGroups, dataEntries []toc.CoordinatorDataEntry, helper int, numHelpers int) []uint32 {
	if backupConfig.SingleDataFile {
		return groups.OidsOfDataFile(dataEntries, helper)
	}
	oids := make([]uint32, 0)
	nextEntryIdx := entriesOfHelper(helper, numHelpers, len(dataEntries))
	for entryIdx, ok := nextEntryIdx(); ok; entryIdx, ok = nextEntryIdx() {
		oids = append(oids, dataEntries[entryIdx].Oid)
	}
	return oids
}

/*
 * entriesOfHelper returns a function that gives the data entries of a
 * gpbackup_helper of a connection, without --single-data-file, in the order in
 * which the helper restores them: those of the helper, the entries helper,
 * helper + numHelpers and so on, out of numEntries.  The connection restores the
 * entries of its own helper only, as only that helper creates their pipes.
 */
func entriesOfHelper(helper int, numHelpers int, numEntries int) func() (int, bool) {
	next := helper
	return func() (int, bool) {
		if helper >= numHelpers || next >= numEntries {
			return 0, false
		}
		entryIdx := next
		next += numHelpers
		return entryIdx, true
	}
}

// lockAncestors locks the given ancestors of a partition in the transaction of the connection
func lockAncestors(ancestors []string, whichConn int) error {
	tables := make([]string, 0, len(ancestors))
	for _, ancestor := range ancestors {
		// Not their other partitions, which LOCK would lock as well
		tables = append(tables, "ONLY "+ancestor)
	}
	_, err := connectionPool.Exec(fmt.Sprintf("LOCK TABLE %s IN ACCESS SHARE MODE", strings.Join(tables, ", ")), whichConn)
	return err
}

/*
 * beginTableTransaction begins the transaction that restores the data of a table,
 * at the default isolation level.  dbconn.Begin would make it SERIALIZABLE, with
 * one more statement for every table, which Greengage logs as it falls back to
 * REPEATABLE READ.
 */
func beginTableTransaction(whichConn int) error {
	tx, err := connectionPool.ConnPool[whichConn].Beginx()
	if err != nil {
		return err
	}
	connectionPool.Tx[whichConn] = tx
	return nil
}

/*
 * numConnsPerDataFile returns the number of connections that restore each data file
 * of a backup together, with numConns connections and the given --copy-queue-size:
 * --copy-queue-size of them, and all of them with a single data file.  With fewer
 * data files to restore than such groups, as after a filter or with more --jobs
 * than data files, the connections are split among the data files instead, so that
 * no group is left without a data file.
 */
func numConnsPerDataFile(dataEntries []toc.CoordinatorDataEntry, numConns int, copyQueueSize int) int {
	filesToRestore := make(map[int]bool)
	for _, entry := range dataEntries {
		filesToRestore[entry.DataFileIdx] = true
	}
	conns := copyQueueSize
	if conns < 1 || conns > numConns {
		conns = numConns
	}
	if len(filesToRestore) > 0 && numConns/conns > len(filesToRestore) {
		conns = numConns / len(filesToRestore)
	}
	return conns
}

/*
 * checkAgentErrors returns an error if the gpbackup_helper with the given index has
 * stopped.  With --single-data-file, several connections may restore the tables of
 * the same helper, so its error file is left for them to find as well.
 */
func checkAgentErrors(fpInfo filepath.FilePathInfo, helperIdx []int) error {
	if backupConfig.SingleDataFile {
		return utils.CheckAgentErrorsOnSegmentsKeepingErrorFile(globalCluster, fpInfo, helperIdx...)
	}
	return utils.CheckAgentErrorsOnSegments(globalCluster, fpInfo, helperIdx...)
}

// The gpbackup_helper of a connection, or with --single-data-file of a data file
func HelperIdx(whichConn int) []int {
	if maxHelpers > 1 {
		return []int{whichConn}
	}
	return []int{}
}

/*
 * DataFileGroups gives each connection the data entries it restores, in order.
 *
 * The gpbackup_helper of a data file reads its tables one after another, so the
 * connections are split into groups of connsPerDataFile, and each data file is
 * restored by one group.  A connection takes the next table of the data file of its
 * group, in the order of the file.  Once all tables of that data file are taken, it
 * takes for its group the largest data file left, by rows, and its first table, so
 * that a group takes a data file only for a connection that is free to restore it,
 * and the groups finish at about the same time.  With a single data file, or
 * without --single-data-file, all connections are one group.
 *
 * A connection never waits for a table further ahead than the pipes its helper has
 * created.  The helper creates the pipes of connsPerDataFile tables ahead of the
 * one it restores, so with LimitAhead, as with a single data file, a connection
 * waits before it takes a table until the table is fewer than connsPerDataFile
 * tables after the first one of its data file whose COPY has not ended.  Otherwise
 * a table whose COPY fails before it reads its pipe, as with --on-error-continue
 * for a table missing from the database, would let its connection go on to tables
 * further on while the helper still restores a table before them, and fail them
 * too once the wait for their pipes timed out.  The helper passes such a table as
 * soon as it reaches it, as its skip file is created before its COPY is recorded
 * as ended.  A table counts until its COPY ends, not until its transaction does,
 * as the helper has passed it by then, and the redistribution of a table with
 * --resize-cluster may take long.  The first table of a data file whose COPY has
 * not ended is being restored by a connection, so it can always proceed.
 * Connections beyond the last full group restore nothing.
 *
 * With OrderTransactions, the transactions that restore the tables of each data
 * file take their turns in the order of the file: each one begins, after the
 * TRUNCATE of its table if any, and locks the ancestors of a partition on GPDB 7
 * once the transactions of the tables before it have done so, or the tables are
 * skipped.  The transaction of a table holds its slot of its resource group and
 * its locks until its data is restored, while its COPY waits for the helper to
 * restore the tables before it.  With resource groups, a transaction waits for a
 * slot as it begins, so that the transactions of later tables could otherwise
 * take all slots while the transaction of an earlier table waited for one, and
 * the helper for its COPY.  On GPDB 7, the COPY into a partition locks all its
 * ancestors, so that a session could otherwise wait for a lock on an ancestor
 * that the transaction of a later table holds, and the transaction of an earlier
 * table for the session as it locks the ancestor: the database cannot see the
 * wait through the helper, so the restore and the session would wait for good.
 * Taking the slots and these locks in the order of the file, the order in which
 * the helper restores the tables, leaves no such cycle.
 *
 * A table takes its own lock with its COPY, after its turn, as before.  A session
 * that locks a table of a data file in SHARE mode and then waits for a later one,
 * whose COPY holds its lock and waits for the helper, can so still wait in a cycle
 * through the helper.  Taking that lock in the turn would close it, but a LOCK is
 * dispatched to the segments and would wait there for any lock of the table on a
 * single segment, which the COPY does not reach while it fails on another segment.
 */
type DataFileGroups struct {
	mutex            sync.Mutex
	connsPerDataFile int
	numGroups        int
	entriesOfFile    [][]int
	filesLeft        []int
	// The data file of each group, -1 before its first one, and its next entry
	fileOfGroup []int
	nextOfGroup []int
	// Called once for each data file, before its first entry is given
	StartDataFile func(file int)
	started       []sync.Once
	// Whether the transactions of the tables of each data file take turns in the order of the file
	OrderTransactions bool
	fileOfEntry       []int
	positionOfEntry   []int
	turnTaken         [][]bool
	nextTurn          []int
	// Whether a table is given only fewer than connsPerDataFile tables after the first
	// one of its data file whose COPY has not ended, and the tables whose COPY has
	LimitAhead     bool
	copyDone       [][]bool
	firstNotCopied []int
	// Broadcast as a turn is taken, the COPY of a table ends, or the context is done
	progressed *sync.Cond
}

func NewDataFileGroups(dataEntries []toc.CoordinatorDataEntry, numDataFiles int, numConns int, connsPerDataFile int) *DataFileGroups {
	groups := &DataFileGroups{
		connsPerDataFile: connsPerDataFile,
		numGroups:        numConns / connsPerDataFile,
		entriesOfFile:    make([][]int, numDataFiles),
		started:          make([]sync.Once, numDataFiles),
		fileOfEntry:      make([]int, len(dataEntries)),
		positionOfEntry:  make([]int, len(dataEntries)),
		turnTaken:        make([][]bool, numDataFiles),
		nextTurn:         make([]int, numDataFiles),
		copyDone:         make([][]bool, numDataFiles),
		firstNotCopied:   make([]int, numDataFiles),
	}
	groups.progressed = sync.NewCond(&groups.mutex)
	groups.fileOfGroup = make([]int, groups.numGroups)
	groups.nextOfGroup = make([]int, groups.numGroups)
	for group := range groups.fileOfGroup {
		groups.fileOfGroup[group] = -1
	}
	rowsOfFile := make([]int64, numDataFiles)
	for entryIdx, entry := range dataEntries {
		groups.fileOfEntry[entryIdx] = entry.DataFileIdx
		groups.positionOfEntry[entryIdx] = len(groups.entriesOfFile[entry.DataFileIdx])
		groups.entriesOfFile[entry.DataFileIdx] = append(groups.entriesOfFile[entry.DataFileIdx], entryIdx)
		groups.turnTaken[entry.DataFileIdx] = append(groups.turnTaken[entry.DataFileIdx], false)
		groups.copyDone[entry.DataFileIdx] = append(groups.copyDone[entry.DataFileIdx], false)
		rowsOfFile[entry.DataFileIdx] += entry.RowsCopied
	}
	for file, entries := range groups.entriesOfFile {
		if len(entries) > 0 {
			groups.filesLeft = append(groups.filesLeft, file)
		}
	}
	sort.SliceStable(groups.filesLeft, func(a, b int) bool {
		return rowsOfFile[groups.filesLeft[a]] > rowsOfFile[groups.filesLeft[b]]
	})
	return groups
}

/*
 * WaitForTurn waits, with OrderTransactions, until the transactions of the tables
 * before the given data entry in its data file have taken their turns, or the
 * tables are skipped.  It returns false if the context is done first.
 */
func (groups *DataFileGroups) WaitForTurn(ctx context.Context, entryIdx int) bool {
	if !groups.OrderTransactions {
		return true
	}
	file, position := groups.fileOfEntry[entryIdx], groups.positionOfEntry[entryIdx]
	groups.mutex.Lock()
	defer groups.mutex.Unlock()
	for groups.nextTurn[file] < position && ctx.Err() == nil {
		groups.progressed.Wait()
	}
	return ctx.Err() == nil
}

// TurnDone records that the transaction of the table of a data entry has taken its turn, or the table is skipped
func (groups *DataFileGroups) TurnDone(entryIdx int) {
	if !groups.OrderTransactions {
		return
	}
	file := groups.fileOfEntry[entryIdx]
	groups.mutex.Lock()
	defer groups.mutex.Unlock()
	groups.turnTaken[file][groups.positionOfEntry[entryIdx]] = true
	for groups.nextTurn[file] < len(groups.turnTaken[file]) && groups.turnTaken[file][groups.nextTurn[file]] {
		groups.nextTurn[file]++
	}
	groups.progressed.Broadcast()
}

// CopyDone records that the COPY of the table of a data entry has ended, or does not run, so that the helper of its
// data file has passed the table or skips it; it may be recorded more than once
func (groups *DataFileGroups) CopyDone(entryIdx int) {
	if !groups.LimitAhead {
		return
	}
	file := groups.fileOfEntry[entryIdx]
	groups.mutex.Lock()
	defer groups.mutex.Unlock()
	groups.copyDone[file][groups.positionOfEntry[entryIdx]] = true
	for groups.firstNotCopied[file] < len(groups.copyDone[file]) && groups.copyDone[file][groups.firstNotCopied[file]] {
		groups.firstNotCopied[file]++
	}
	groups.progressed.Broadcast()
}

// WakeWhenDone wakes the connections waiting for their turns or tables once the context is done
func (groups *DataFileGroups) WakeWhenDone(ctx context.Context) {
	if !groups.OrderTransactions && !groups.LimitAhead {
		return
	}
	go func() {
		<-ctx.Done()
		groups.mutex.Lock()
		defer groups.mutex.Unlock()
		groups.progressed.Broadcast()
	}()
}

// The oids of the data entries of a data file
func (groups *DataFileGroups) OidsOfDataFile(dataEntries []toc.CoordinatorDataEntry, file int) []uint32 {
	oids := make([]uint32, 0, len(groups.entriesOfFile[file]))
	for _, entryIdx := range groups.entriesOfFile[file] {
		oids = append(oids, dataEntries[entryIdx].Oid)
	}
	return oids
}

// The next data entry of a group and its data file, which the group takes once all
// entries of its data file are taken, if one is left and the context is not done
func (groups *DataFileGroups) nextEntryOfGroup(ctx context.Context, group int) (int, int, bool) {
	groups.mutex.Lock()
	defer groups.mutex.Unlock()
	file := groups.fileOfGroup[group]
	if file < 0 || groups.nextOfGroup[group] == len(groups.entriesOfFile[file]) {
		if len(groups.filesLeft) == 0 {
			return 0, 0, false
		}
		file = groups.filesLeft[0]
		groups.filesLeft = groups.filesLeft[1:]
		groups.fileOfGroup[group] = file
		groups.nextOfGroup[group] = 0
	}
	entryIdx := groups.entriesOfFile[file][groups.nextOfGroup[group]]
	groups.nextOfGroup[group]++
	for groups.LimitAhead && groups.positionOfEntry[entryIdx]-groups.firstNotCopied[file] >= groups.connsPerDataFile && ctx.Err() == nil {
		groups.progressed.Wait()
	}
	return entryIdx, file, ctx.Err() == nil
}

// EntriesOfConn returns a function that gives the next data entry of a connection
func (groups *DataFileGroups) EntriesOfConn(ctx context.Context, whichConn int) func() (int, bool) {
	group := whichConn / groups.connsPerDataFile
	return func() (int, bool) {
		if group >= groups.numGroups {
			return 0, false
		}
		entryIdx, file, ok := groups.nextEntryOfGroup(ctx, group)
		if ok && groups.StartDataFile != nil {
			// Outside of the mutex, as starting a helper takes commands on the segments
			groups.started[file].Do(func() { groups.StartDataFile(file) })
		}
		return entryIdx, ok
	}
}
